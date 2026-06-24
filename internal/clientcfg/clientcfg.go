// Package clientcfg persists client-side settings (things the CLI/TUI need that
// the daemon doesn't), stored at ~/.dejima/client.json. It is intentionally
// separate from per-island project configs and from daemon state.
package clientcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aoos/dejima/internal/paths"
)

// Profile is a saved connection target. Host is "" for the local Unix socket,
// or "host:port" for a remote daemon reached over TCP.
type Profile struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
}

// Config holds client-side preferences.
type Config struct {
	// RepoRoot is the directory the TUI repo picker scans for git repos.
	// Empty means the user hasn't chosen one yet (first-load prompt pending).
	RepoRoot string `json:"repo_root,omitempty"`

	// Profiles are saved connection targets (local + remote daemons), switchable
	// from the TUI. ActiveProfile records the last one selected.
	Profiles      []Profile `json:"profiles,omitempty"`
	ActiveProfile string    `json:"active_profile,omitempty"`

	// Editor is the CLI command for the user's preferred Remote-SSH editor
	// (e.g. "code", "cursor", "windsurf", "antigravity"). Empty means auto-detect
	// the first one on PATH. Set from the TUI settings (',').
	Editor string `json:"editor,omitempty"`
}

// ActiveHost resolves the currently-active profile to its daemon host. ok is
// false when the active profile is the local socket, unset, or *dangling* — i.e.
// ActiveProfile names a profile that no longer exists (e.g. it was deleted while
// still selected). A dangling reference resolves to local rather than wedging
// the client on a target it can't look up.
func (c Config) ActiveHost() (host string, ok bool) {
	if c.ActiveProfile == "" || c.ActiveProfile == "local" {
		return "", false
	}
	for _, p := range c.Profiles {
		if p.Name == c.ActiveProfile {
			return p.Host, true
		}
	}
	return "", false
}

// LookupProfile resolves a profile by name to its daemon host, reading nothing
// but the in-memory config. The synthetic "local" name (and "") resolve to the
// empty host (the local Unix socket). This is the *ephemeral* lookup behind the
// `-p/--profile` launch flag: it never touches ActiveProfile, so selecting a
// profile for one invocation can't stomp the persistent choice shared by
// concurrent TUIs. An unknown name is an error (vs ActiveHost's silent
// fall-back), because an explicit `-p typo` should fail loudly, not connect
// somewhere unexpected.
func (c Config) LookupProfile(name string) (host string, err error) {
	if name == "" || name == "local" {
		return "", nil
	}
	for _, p := range c.Profiles {
		if p.Name == name {
			return p.Host, nil
		}
	}
	return "", fmt.Errorf("no profile named %q (use `dejima profile ls` to see saved profiles)", name)
}

// AddProfile saves a new connection target and persists it. It is the store
// half of the TUI add-flow, factored out so the CLI (`dejima profile add`) and
// the TUI share one code path. A duplicate name is rejected so two profiles
// can't shadow each other in lookups.
func AddProfile(name, host string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if name == "local" {
		return fmt.Errorf("%q is reserved for the local socket", name)
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	for _, p := range cfg.Profiles {
		if p.Name == name {
			return fmt.Errorf("a profile named %q already exists", name)
		}
	}
	cfg.Profiles = append(cfg.Profiles, Profile{Name: name, Host: host})
	return Save(cfg)
}

// SwitchProfile persists name as the active profile — the same write the TUI
// switcher makes. Unlike LookupProfile (ephemeral), this *does* mutate
// active_profile in client.json, which is the whole point of `switch` vs `-p`.
// The synthetic "local" clears the active profile (back to the Unix socket).
// An unknown name is rejected so `switch` can't leave a dangling reference.
func SwitchProfile(name string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	if name == "" || name == "local" {
		cfg.ActiveProfile = ""
		return Save(cfg)
	}
	found := false
	for _, p := range cfg.Profiles {
		if p.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no profile named %q (use `dejima profile ls` to see saved profiles)", name)
	}
	cfg.ActiveProfile = name
	return Save(cfg)
}

func configPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "client.json"), nil
}

// Load reads the client config. A missing file yields a zero Config, no error.
func Load() (Config, error) {
	var c Config
	p, err := configPath()
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// Save writes the client config.
func Save(c Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
