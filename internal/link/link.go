// Package link is the inter-island exchange broker — Lane 5, Phase 2 of
// docs/inter-island-exchange-spec.md. Cross-island is deny-all by default
// (containment); an info channel exists ONLY as an explicit, operator-granted,
// directional A→B grant on a named topic. The daemon is the only broker: there
// is no island↔island socket, every message flows through dejimad, and every
// grant/message/denial is ledgered.
//
// This package owns only the authorization: the persisted Grant store. Delivery
// is NOT here — a granted cross-island message is delivered into the *recipient
// agent's ordinary mailbox* (internal/mailbox), stamped with daemon-controlled
// provenance, so there is a single durable inbox and a single audit surface. The
// HTTP gate in internal/api enforces "an island may only send AS itself, and only
// on a granted channel" — agents request, never self-approve. Action delegation
// (one island causing another to *act*) is the separate, harder-gated Phase 3 and
// is NOT here: a Phase-2 grant moves info, never actions.
package link

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

	"github.com/aoos/dejima/internal/paths"
)

// Grant is an operator-authorized, directional info channel from island From to
// island To on a named Topic. Actions is reserved for Phase 3 (the per-action
// allowlist); in Phase 2 a grant is info-only and Actions is empty. The grant is
// island→island — the target *agent* is a delivery address chosen per message,
// not part of the grant (the island is the security boundary the operator grants).
type Grant struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Topic     string    `json:"topic"`
	Actions   []string  `json:"actions,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func grantKey(from, to, topic string) string {
	return from + "\x00" + to + "\x00" + topic
}

// Key is this grant's unique identity (from, to, topic).
func (g Grant) Key() string { return grantKey(g.From, g.To, g.Topic) }

// Store is the per-daemon set of link grants. Operator-only and deny-all: a
// channel that isn't in the store does not exist. Concurrency-safe; persisted
// atomically (mirrors internal/githubid).
type Store struct {
	Grants map[string]Grant `json:"grants"`
}

var mu sync.Mutex

func storePath() (string, error) {
	dir, err := paths.LinksDir()
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
		return &Store{Grants: map[string]Grant{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse link store: %w", err)
	}
	if s.Grants == nil {
		s.Grants = map[string]Grant{}
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

// Grant adds or replaces a grant (keyed by from/to/topic), stamping CreatedAt.
func (s *Store) Grant(g Grant) Grant {
	if s.Grants == nil {
		s.Grants = map[string]Grant{}
	}
	g.From, g.To, g.Topic = strings.TrimSpace(g.From), strings.TrimSpace(g.To), strings.TrimSpace(g.Topic)
	g.CreatedAt = time.Now().UTC()
	s.Grants[g.Key()] = g
	return g
}

// Revoke removes a grant. Returns false when no such grant exists.
func (s *Store) Revoke(from, to, topic string) bool {
	k := grantKey(strings.TrimSpace(from), strings.TrimSpace(to), strings.TrimSpace(topic))
	if _, ok := s.Grants[k]; !ok {
		return false
	}
	delete(s.Grants, k)
	return true
}

// Allowed reports whether a from→to message on topic is authorized, returning
// the matching grant. Default-deny: no grant ⇒ (Grant{}, false).
func (s *Store) Allowed(from, to, topic string) (Grant, bool) {
	g, ok := s.Grants[grantKey(strings.TrimSpace(from), strings.TrimSpace(to), strings.TrimSpace(topic))]
	return g, ok
}

// List returns every grant, sorted by from/to/topic.
func (s *Store) List() []Grant {
	out := make([]Grant, 0, len(s.Grants))
	for _, g := range s.Grants {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
