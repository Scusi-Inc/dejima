// Package spawn is the operator-granted budget for agent-initiated EPHEMERAL
// sub-agents — the governance layer that makes agent spawning a gated capability
// rather than a free-for-all (the differentiator vs free programmatic spawn).
//
// This package is the DATA LAYER only (slice 1): the persisted per-island grant +
// its budget. Default posture is deny — an island with no grant cannot spawn at
// all (the spawn route isn't reachable by an in-island token). An operator adds a
// grant with explicit limits; the spawn handler (a later slice) enforces them and
// ledgers each spawn. Granting is OPERATOR-ONLY — an in-island token can never
// create or modify a grant (only spawn within one).
package spawn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// Grant is one island's ephemeral-sub-agent budget. Zero/empty fields mean
// "unset" per the field docs; an island with no Grant at all cannot spawn.
type Grant struct {
	Island string `json:"island"`
	// MaxConcurrent caps live ephemeral sub-agents at once (the primary safety
	// limit). Must be > 0 for a grant to permit any spawning.
	MaxConcurrent int `json:"max_concurrent"`
	// MaxTotal caps lifetime spawns for this grant; 0 = unlimited within the grant.
	MaxTotal int `json:"max_total,omitempty"`
	// Types restricts which agent types may be spawned; empty = any spawnable type.
	Types []string `json:"types,omitempty"`
	// TTL is each sub-agent's max lifetime before it's reaped; 0 = no time cap
	// (still reaped on exit / parent removal / revoke).
	TTL time.Duration `json:"ttl,omitempty"`
	// PerAgentMemory / PerAgentCPUs cap EACH sub-agent's resources, so a granted
	// orchestrator can't spawn N workers that collectively OOM the host (see the
	// resource-vs-cap signals). Docker-style strings, e.g. "512m" / "1.0". Empty =
	// inherit the daemon/island default.
	PerAgentMemory string `json:"per_agent_memory,omitempty"`
	PerAgentCPUs   string `json:"per_agent_cpus,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// Store is the persisted set of per-island grants, keyed by island name.
type Store struct {
	Grants map[string]Grant `json:"grants"`
}

var mu sync.Mutex

func storePath() (string, error) {
	dir, err := paths.SpawnDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store.json"), nil
}

// Update runs fn against the store under a process-wide lock and persists the
// result atomically — use it for every read-modify-write (grant/revoke).
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

// Load returns a snapshot of the store (empty when none exists yet).
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
		return nil, fmt.Errorf("parse spawn store: %w", err)
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

// Get returns the grant for an island (ok=false when none — the deny default).
func (s *Store) Get(island string) (Grant, bool) {
	g, ok := s.Grants[island]
	return g, ok
}

// Set upserts a grant, stamping CreatedAt. MaxConcurrent must be > 0 (a grant
// that permits nothing is a revoke). Use within Update.
func (s *Store) Set(g Grant) error {
	if g.Island == "" {
		return errors.New("island is required")
	}
	if g.MaxConcurrent <= 0 {
		return errors.New("max_concurrent must be > 0 (use revoke to remove a grant)")
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	if s.Grants == nil {
		s.Grants = map[string]Grant{}
	}
	s.Grants[g.Island] = g
	return nil
}

// Remove deletes an island's grant (revoke). Returns whether one existed.
func (s *Store) Remove(island string) bool {
	if _, ok := s.Grants[island]; !ok {
		return false
	}
	delete(s.Grants, island)
	return true
}

// List returns all grants.
func (s *Store) List() []Grant {
	out := make([]Grant, 0, len(s.Grants))
	for _, g := range s.Grants {
		out = append(out, g)
	}
	return out
}
