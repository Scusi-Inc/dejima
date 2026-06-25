// Package policy is the action-gate's auto-approve layer (docs/action-gate-spec.md
// §"Policy layer"). The default posture is prompt-everything; an operator opts
// INTO automation by adding scoped, expiring, counted auto-approve rules for a
// specific link (from→to→topic) + action-type. Two hard guarantees:
//
//   - Destructive actions are NEVER auto-approved — Consume filters on the
//     request's Tier, so no rule can become a wipe-the-drive bypass.
//   - Rules are bounded (count + TTL) and revocable; creating/removing one is
//     privileged and ledgered by the caller (a blanket auto-approve is itself a
//     bypass vector).
//
// It imports internal/link for ActionRequest/Tier; link must not import this
// package (the api layer wires the two together).
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/paths"
)

// Rule auto-approves a specific action-type over a specific link, up to MaxCount
// times, until ExpiresAt. It never covers destructive actions (enforced at
// Consume, not stored here — the tier is a property of each request).
type Rule struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Topic     string    `json:"topic"`
	Action    string    `json:"action"`
	MaxCount  int       `json:"max_count"` // <=0 = unlimited within the TTL window
	Used      int       `json:"used"`
	ExpiresAt time.Time `json:"expires_at,omitempty"` // zero = no expiry
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

func ruleKey(from, to, topic, action string) string {
	return from + "\x00" + to + "\x00" + topic + "\x00" + action
}

// Key is the rule's unique identity (from, to, topic, action).
func (r Rule) Key() string { return ruleKey(r.From, r.To, r.Topic, r.Action) }

func (r Rule) expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
}
func (r Rule) exhausted() bool { return r.MaxCount > 0 && r.Used >= r.MaxCount }

// Remaining is the budget left (-1 = unlimited within the TTL).
func (r Rule) Remaining() int {
	if r.MaxCount <= 0 {
		return -1
	}
	if n := r.MaxCount - r.Used; n > 0 {
		return n
	}
	return 0
}

// Store is the per-daemon set of auto-approve rules. Persisted atomically;
// mirrors internal/link's grant store.
type Store struct {
	Rules map[string]Rule `json:"rules"`
}

var mu sync.Mutex

func storePath() (string, error) {
	dir, err := paths.PolicyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store.json"), nil
}

// Update runs fn against the store under a process-wide lock and persists the
// result atomically. Use it for every read-modify-write.
func Update(fn func(*Store) error) (*Store, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := loadLocked()
	if err != nil {
		return nil, err
	}
	if err := fn(s); err != nil {
		return nil, err
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load returns a consistent snapshot of the store (empty when none exists yet).
func Load() (*Store, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked()
}

func loadLocked() (*Store, error) {
	p, err := storePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Rules: map[string]Rule{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse policy store: %w", err)
	}
	if s.Rules == nil {
		s.Rules = map[string]Rule{}
	}
	return &s, nil
}

func (s *Store) saveLocked() error {
	p, err := storePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".store-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

// Add inserts or replaces a rule (keyed by from/to/topic/action), stamping
// CreatedAt and computing ExpiresAt from ttl (ttl<=0 = no expiry). Used resets.
func (s *Store) Add(r Rule, ttl time.Duration) Rule {
	if s.Rules == nil {
		s.Rules = map[string]Rule{}
	}
	r.From, r.To, r.Topic, r.Action = strings.TrimSpace(r.From), strings.TrimSpace(r.To), strings.TrimSpace(r.Topic), strings.TrimSpace(r.Action)
	now := time.Now().UTC()
	r.CreatedAt = now
	r.Used = 0
	if ttl > 0 {
		r.ExpiresAt = now.Add(ttl)
	} else {
		r.ExpiresAt = time.Time{}
	}
	s.Rules[r.Key()] = r
	return r
}

// Remove deletes a rule by its parts. Returns false when no such rule exists.
func (s *Store) Remove(from, to, topic, action string) bool {
	k := ruleKey(strings.TrimSpace(from), strings.TrimSpace(to), strings.TrimSpace(topic), strings.TrimSpace(action))
	if _, ok := s.Rules[k]; !ok {
		return false
	}
	delete(s.Rules, k)
	return true
}

// List returns every rule (newest first), pruning expired ones as it goes.
func (s *Store) List() []Rule {
	now := time.Now().UTC()
	out := make([]Rule, 0, len(s.Rules))
	for k, r := range s.Rules {
		if r.expired(now) {
			delete(s.Rules, k)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Consume finds a live rule that auto-approves ar and spends one unit of its
// budget (persisted), returning the rule + true. It returns false — meaning the
// caller must queue the action for human approval — when no rule matches, the
// matching rule is expired/exhausted, OR the action is destructive (which no
// rule may ever auto-approve). Expired matches are pruned.
func Consume(ar link.ActionRequest) (Rule, bool, error) {
	// The non-negotiable backstop: destructive is never auto-approvable.
	if !ar.Tier.AutoApprovable() {
		return Rule{}, false, nil
	}
	var matched Rule
	var ok bool
	_, err := Update(func(s *Store) error {
		now := time.Now().UTC()
		k := ruleKey(strings.TrimSpace(ar.From), strings.TrimSpace(ar.To), strings.TrimSpace(ar.Topic), strings.TrimSpace(ar.Action))
		r, found := s.Rules[k]
		if !found {
			return nil
		}
		if r.expired(now) {
			delete(s.Rules, k) // prune
			return nil
		}
		if r.exhausted() {
			return nil
		}
		r.Used++
		s.Rules[k] = r
		matched, ok = r, true
		return nil
	})
	if err != nil {
		return Rule{}, false, err
	}
	return matched, ok, nil
}
