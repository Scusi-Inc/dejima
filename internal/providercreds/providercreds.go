// Package providercreds manages the daemon's set of LLM provider credentials:
// named (provider, api_key, optional base_url) entries that agent frameworks
// inside islands use to reach a model. Keyed by provider id (the left side of a
// "provider/model" string, e.g. "openai", "anthropic"), the store is
// account-wide and reusable — one "anthropic" key serves every island whose
// agents target Anthropic. The daemon is the credential owner; a key is supplied
// once (`dejima provider set`) and materialized per-island into a single-provider
// .env file mounted read-only (see DotEnv / paths.LLMIslandConfigDir), never the
// whole set and never as a container env var.
//
// This is the LLM-provider analog of internal/githubid: same locked,
// atomic-write store, same "materialize only what the island needs" posture. The
// key is a secret and is NEVER returned to clients — List() yields key-less Meta.
package providercreds

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

// Provider is one LLM provider credential the daemon can supply to islands.
type Provider struct {
	Name      string    `json:"name"`                 // provider id == "provider/model" prefix: "openai","anthropic","my-openrouter"
	APIKey    string    `json:"api_key"`              // secret — never returned to clients
	BaseURL   string    `json:"base_url,omitempty"`   // optional endpoint override (proxies, Azure, self-host)
	EnvVar    string    `json:"env_var,omitempty"`    // override the derived env name; default UPPER(Name)+"_API_KEY"
	UpdatedAt time.Time `json:"updated_at,omitempty"` // last time the key was set
}

// Meta is a Provider without its key: the safe view to hand back to clients. The
// key itself is replaced by a masked Hint (last few chars only).
type Meta struct {
	Name      string    `json:"name"`
	EnvVar    string    `json:"env_var"`
	BaseURL   string    `json:"base_url,omitempty"`
	Default   bool      `json:"default"`
	KeySet    bool      `json:"key_set"`
	Hint      string    `json:"hint,omitempty"` // masked tail, e.g. "…a1b2" — never the full key
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Store is the per-daemon provider set with one default.
type Store struct {
	Default   string              `json:"default"`
	Providers map[string]Provider `json:"providers"`
}

func storePath() (string, error) {
	dir, err := paths.LLMSecretsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store.json"), nil
}

// mu serializes the store across the daemon's request goroutines so a
// read-modify-write (Update) is atomic and a Load never observes a torn write.
var mu sync.Mutex

// Update runs fn against the store under a process-wide lock and persists the
// result atomically. Use it for every read-modify-write — Put/Remove/SetDefault.
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
		return &Store{Providers: map[string]Provider{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse provider credential store: %w", err)
	}
	if s.Providers == nil {
		s.Providers = map[string]Provider{}
	}
	return &s, nil
}

// Save persists the store atomically at 0600 (it holds API keys).
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

// Put adds or updates a provider (keyed by Name), stamping UpdatedAt. The first
// provider added becomes the default.
func (s *Store) Put(p Provider) {
	if s.Providers == nil {
		s.Providers = map[string]Provider{}
	}
	p.Name = strings.TrimSpace(p.Name)
	p.UpdatedAt = time.Now()
	s.Providers[p.Name] = p
	if s.Default == "" {
		s.Default = p.Name
	}
}

// Remove deletes a provider. If it was the default, the default falls to the
// first remaining provider (by name), or is cleared when none remain.
func (s *Store) Remove(name string) bool {
	if _, ok := s.Providers[name]; !ok {
		return false
	}
	delete(s.Providers, name)
	if s.Default == name {
		s.Default = ""
		if list := s.List(); len(list) > 0 {
			s.Default = list[0].Name
		}
	}
	return true
}

// SetDefault marks name as the default provider.
func (s *Store) SetDefault(name string) error {
	if _, ok := s.Providers[name]; !ok {
		return fmt.Errorf("no such provider %q", name)
	}
	s.Default = name
	return nil
}

// Resolve returns the provider for name, or the default when name is empty. ok
// is false when nothing matches (unknown name, or empty name with no default).
func (s *Store) Resolve(name string) (Provider, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = s.Default
	}
	if name == "" {
		return Provider{}, false
	}
	p, ok := s.Providers[name]
	return p, ok
}

// List returns provider metadata (no keys, masked hint only), sorted by name.
func (s *Store) List() []Meta {
	out := make([]Meta, 0, len(s.Providers))
	for _, p := range s.Providers {
		out = append(out, Meta{
			Name:      p.Name,
			EnvVar:    EnvVarName(p),
			BaseURL:   p.BaseURL,
			Default:   p.Name == s.Default,
			KeySet:    p.APIKey != "",
			Hint:      maskHint(p.APIKey),
			UpdatedAt: p.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// EnvVarName is the environment variable a framework reads this provider's key
// from: the explicit EnvVar override, else the de-facto convention
// UPPER(Name)+"_API_KEY" (e.g. "anthropic" → ANTHROPIC_API_KEY). Provider names
// aren't always valid env identifiers (e.g. "my-openrouter"), so non-alnum runs
// collapse to underscores.
func EnvVarName(p Provider) string {
	if v := strings.TrimSpace(p.EnvVar); v != "" {
		return v
	}
	return envIdent(p.Name) + "_API_KEY"
}

// DotEnv renders the generic KEY=value env file the daemon materializes per
// island and the per-agent shim sources. It carries the provider key under its
// env-var name and, when set, a "<PREFIX>_BASE_URL" override (PREFIX is the
// env-var name minus a trailing _API_KEY, matching the OPENAI_BASE_URL /
// ANTHROPIC_BASE_URL convention). It never contains anything but these.
func DotEnv(p Provider) string {
	var b strings.Builder
	env := EnvVarName(p)
	fmt.Fprintf(&b, "%s=%s\n", env, p.APIKey)
	if url := strings.TrimSpace(p.BaseURL); url != "" {
		prefix := strings.TrimSuffix(env, "_API_KEY")
		fmt.Fprintf(&b, "%s_BASE_URL=%s\n", prefix, url)
	}
	return b.String()
}

// envIdent upper-cases name and replaces any run of non-alphanumeric characters
// with a single underscore, so a store handle like "my-openrouter" becomes a
// legal env prefix "MY_OPENROUTER".
func envIdent(name string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToUpper(strings.TrimSpace(name)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// maskHint returns a non-reversible hint of a key for display: the last 4 chars
// prefixed with an ellipsis, or "set" when the key is too short to safely show a
// tail. Empty for an empty key.
func maskHint(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "set"
	}
	return "…" + key[len(key)-4:]
}
