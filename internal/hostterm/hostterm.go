// Package hostterm manages the daemon's host terminals: operator shells that run
// in tmux ON THE DAEMON HOST (not in a container). They are the most privileged
// surface in the system — uncontained, operator-only, flag-gated, audited, and
// never reachable by an island token (see internal/api/tokenauth.go and
// docs/host-terminals.md). This package is just the persisted registry; the tmux
// lifecycle and PTY attach live in internal/api + internal/bridge.
package hostterm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// Terminal is one host terminal: a named tmux session on the daemon host.
type Terminal struct {
	ID        string    `json:"id"` // "t1", "t2", …
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Tmux is the host tmux session name backing this terminal.
func (t Terminal) Tmux() string { return "dejima-term-" + t.ID }

// Store is the persisted set of host terminals.
type Store struct {
	Terminals []Terminal `json:"terminals"`
}

// mu serializes the store so read-modify-writes (Update) are atomic and a Load
// never observes a torn write.
var mu sync.Mutex

func storePath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "host-terminals.json"), nil
}

// Load reads the store under the lock; returns an empty store if none exists.
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
		return &Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse host-terminals store: %w", err)
	}
	return &s, nil
}

// Update runs fn against the store under the lock and persists it atomically.
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

func (s *Store) saveLocked() error {
	p, err := storePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".host-terminals-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

// NextID returns the next monotonic "t<N>" id not currently in use.
func (s *Store) NextID() string {
	max := 0
	for _, t := range s.Terminals {
		if n, ok := parseTermID(t.ID); ok && n > max {
			max = n
		}
	}
	return fmt.Sprintf("t%d", max+1)
}

func parseTermID(id string) (int, bool) {
	if len(id) < 2 || id[0] != 't' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// Add appends a terminal (caller sets ID via NextID and stamps CreatedAt).
func (s *Store) Add(t Terminal) { s.Terminals = append(s.Terminals, t) }

// Remove deletes a terminal by id.
func (s *Store) Remove(id string) bool {
	for i, t := range s.Terminals {
		if t.ID == id {
			s.Terminals = append(s.Terminals[:i], s.Terminals[i+1:]...)
			return true
		}
	}
	return false
}

// Get returns the terminal with the given id.
func (s *Store) Get(id string) (Terminal, bool) {
	for _, t := range s.Terminals {
		if t.ID == id {
			return t, true
		}
	}
	return Terminal{}, false
}

// Relabel sets a terminal's label.
func (s *Store) Relabel(id, label string) bool {
	for i := range s.Terminals {
		if s.Terminals[i].ID == id {
			s.Terminals[i].Label = strings.TrimSpace(label)
			return true
		}
	}
	return false
}
