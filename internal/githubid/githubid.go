// Package githubid manages the daemon's set of GitHub identities: named
// (login, host, token) triples that islands clone and push as. The daemon is
// the credential owner, so islands work from any client device — including ones
// with no gh of their own. A client that does have gh can seed the store with
// `dejima auth push --github`. Each island selects one identity (or the
// default) at create time; the daemon materializes just that identity into the
// island's gh config (see HostsYAML), never the whole set.
package githubid

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aoos/dejima/internal/paths"
)

// DefaultHost is the GitHub host assumed when an identity doesn't name one.
const DefaultHost = "github.com"

// Identity is one GitHub login the daemon can act as.
type Identity struct {
	Name  string `json:"name"`  // dejima-local handle: "work", "personal", …
	Login string `json:"login"` // GitHub username
	Host  string `json:"host"`  // "github.com" or an enterprise host
	Token string `json:"token"` // OAuth/PAT token — secret, never returned to clients
}

// Meta is an identity without its token: the safe view to hand back to clients.
type Meta struct {
	Name    string `json:"name"`
	Login   string `json:"login"`
	Host    string `json:"host"`
	Default bool   `json:"default"`
}

// Store is the per-daemon identity set with one default.
type Store struct {
	Default    string              `json:"default"`
	Identities map[string]Identity `json:"identities"`
}

func storePath() (string, error) {
	dir, err := paths.GitHubSecretsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store.json"), nil
}

// mu serializes the store across the daemon's request goroutines so a
// read-modify-write (Update) is atomic and a Load never observes a torn write.
var mu sync.Mutex

// Update runs fn against the store under a process-wide lock and persists the
// result atomically. Use it for every read-modify-write — Put/Remove/SetDefault
// — so concurrent writers can't clobber each other (lost updates).
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

// Load reads the store under the lock — a consistent snapshot for read-only use.
// Returns an empty (non-nil) store if none exists yet.
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
		return &Store{Identities: map[string]Identity{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse github identity store: %w", err)
	}
	if s.Identities == nil {
		s.Identities = map[string]Identity{}
	}
	return &s, nil
}

// Save persists the store atomically at 0600 (it holds tokens).
func (s *Store) Save() error {
	mu.Lock()
	defer mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes the store via a temp file + rename so a crash or a
// concurrent reader never sees a half-written file. Caller holds mu.
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
	defer os.Remove(tmpName) // harmless once the rename has consumed it
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

// Put adds or updates an identity (keyed by Name). The first identity added
// becomes the default.
func (s *Store) Put(id Identity) {
	if s.Identities == nil {
		s.Identities = map[string]Identity{}
	}
	if strings.TrimSpace(id.Host) == "" {
		id.Host = DefaultHost
	}
	s.Identities[id.Name] = id
	if s.Default == "" {
		s.Default = id.Name
	}
}

// Remove deletes an identity. If it was the default, the default falls to the
// first remaining identity (by name), or is cleared when none remain.
func (s *Store) Remove(name string) bool {
	if _, ok := s.Identities[name]; !ok {
		return false
	}
	delete(s.Identities, name)
	if s.Default == name {
		s.Default = ""
		if list := s.List(); len(list) > 0 {
			s.Default = list[0].Name
		}
	}
	return true
}

// SetDefault marks name as the default identity.
func (s *Store) SetDefault(name string) error {
	if _, ok := s.Identities[name]; !ok {
		return fmt.Errorf("no such github identity %q", name)
	}
	s.Default = name
	return nil
}

// Resolve returns the identity for name, or the default when name is empty. ok
// is false when nothing matches (unknown name, or empty name with no default).
func (s *Store) Resolve(name string) (Identity, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = s.Default
	}
	if name == "" {
		return Identity{}, false
	}
	id, ok := s.Identities[name]
	return id, ok
}

// List returns identity metadata (no tokens), sorted by name.
func (s *Store) List() []Meta {
	out := make([]Meta, 0, len(s.Identities))
	for _, id := range s.Identities {
		out = append(out, Meta{Name: id.Name, Login: id.Login, Host: id.Host, Default: id.Name == s.Default})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HostsYAML renders the gh hosts.yml for a single identity — what gh reads from
// GH_CONFIG_DIR to authenticate git over HTTPS inside an island. Only ever one
// identity per file.
//
// It MUST emit the modern multi-account schema (the per-user `users:` map), not
// just the legacy top-level form. gh migrates a legacy-only hosts.yml to this
// schema on first use, which means writing back to GH_CONFIG_DIR — but the
// daemon mounts that dir read-only, so the migration fails and `gh auth
// setup-git` errors out, leaving the island with no git credential helper (the
// clone then can't authenticate). Materializing the already-migrated form means
// gh has nothing to write. See also ConfigYAML, which supplies the version
// marker gh checks before deciding to migrate.
func HostsYAML(id Identity) string {
	host := strings.TrimSpace(id.Host)
	if host == "" {
		host = DefaultHost
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", host)
	// Modern per-user entry (what gh 2.x keys off for multi-account).
	b.WriteString("    users:\n")
	fmt.Fprintf(&b, "        %s:\n", id.Login)
	fmt.Fprintf(&b, "            oauth_token: %s\n", id.Token)
	// Legacy top-level keys, kept for backward compatibility with older gh.
	fmt.Fprintf(&b, "    oauth_token: %s\n", id.Token)
	fmt.Fprintf(&b, "    user: %s\n", id.Login)
	b.WriteString("    git_protocol: https\n")
	return b.String()
}

// ConfigYAML renders the gh config.yml that accompanies HostsYAML. Its only job
// is to carry the schema version marker: with it present, gh treats the
// materialized config as already-migrated and never tries to write to the
// read-only GH_CONFIG_DIR mount. Without it, gh runs a migration on first use
// and fails on the read-only dir (see HostsYAML).
func ConfigYAML() string {
	return "version: \"1\"\n"
}
