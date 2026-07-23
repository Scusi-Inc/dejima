// Package clientcfg persists client-side settings (things the CLI/TUI need that
// the daemon doesn't), stored at ~/.dejima/client.json. It is intentionally
// separate from per-island project configs and from daemon state.
package clientcfg

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoos/dejima/internal/invite"
	"github.com/aoos/dejima/internal/paths"
)

// Profile is a saved connection target. Host is "" for the local Unix socket,
// or "host:port" for a remote daemon reached over TCP. Token is the bearer
// secret for that target — a team invite persists it here so the teammate
// doesn't re-export DEJIMA_TOKEN every session; Role/Islands are the invite's
// echo, kept for display only (the daemon enforces the real scope). The file is
// written 0600 (see Save), matching the per-island token files' posture.
type Profile struct {
	Name    string   `json:"name"`
	Host    string   `json:"host,omitempty"`
	Token   string   `json:"token,omitempty"`
	Role    string   `json:"role,omitempty"`
	Islands []string `json:"islands,omitempty"`
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

// RemoveProfile deletes a saved profile by name and persists. If it was the
// active profile, ActiveProfile is cleared (back to the local socket) so no
// dangling reference is left. It's the store half of both the TUI delete and the
// CLI `dejima profile rm`, so the two share one code path. "local" can't be
// removed (it's the synthetic socket target), and an unknown name errors loudly
// rather than silently no-op'ing.
func RemoveProfile(name string) error {
	if name == "" || name == "local" {
		return fmt.Errorf("%q is the local socket and can't be removed", name)
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	kept := cfg.Profiles[:0]
	found := false
	for _, p := range cfg.Profiles {
		if p.Name == name {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		return fmt.Errorf("no profile named %q (use `dejima profile ls` to see saved profiles)", name)
	}
	cfg.Profiles = kept
	if cfg.ActiveProfile == name {
		cfg.ActiveProfile = ""
	}
	return Save(cfg)
}

// TokenForHost returns the bearer token saved for a connection host, preferring
// the active profile when it matches (so two profiles on the same host don't
// race). It is the no-env-token default behind the connection path: an explicit
// DEJIMA_TOKEN still wins upstream. Empty when no saved profile carries a token
// for that host (e.g. the local socket, or a host added without one).
func (c Config) TokenForHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if c.ActiveProfile != "" {
		for _, p := range c.Profiles {
			if p.Name == c.ActiveProfile && p.Host == host && p.Token != "" {
				return p.Token
			}
		}
	}
	for _, p := range c.Profiles {
		if p.Host == host && p.Token != "" {
			return p.Token
		}
	}
	return ""
}

// SaveInvite persists a decoded team invite as a connection profile and makes it
// active, so the teammate is connected on the next command with no env vars. The
// profile name defaults to the invite's Name, else the host's first DNS label; a
// name clash updates that profile in place (re-joining rotates the saved token
// rather than erroring). Returns the resolved profile name. The bearer secret
// lands in client.json, which Save writes 0600.
func SaveInvite(p invite.Payload) (string, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = hostLabel(p.Host)
	}
	if name == "" {
		return "", fmt.Errorf("invite has no name and no usable host to derive one")
	}
	if name == "local" {
		return "", fmt.Errorf("%q is reserved for the local socket", name)
	}
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	prof := Profile{Name: name, Host: strings.TrimSpace(p.Host), Token: p.Token, Role: p.Role, Islands: p.Islands}
	updated := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == name {
			cfg.Profiles[i] = prof
			updated = true
			break
		}
	}
	if !updated {
		cfg.Profiles = append(cfg.Profiles, prof)
	}
	cfg.ActiveProfile = name
	return name, Save(cfg)
}

// hostLabel derives a friendly profile name from a host:port — the first DNS
// label (or the bare host/IP when there's no dot/port).
func hostLabel(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[:i]
	}
	return host
}

func configPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "client.json"), nil
}

// Path returns the client config file location (~/.dejima/client.json) without
// creating it — for a client uninstall that removes it.
func Path() (string, error) { return configPath() }

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
		// client.json is corrupt. Try the last-known-good backup so a botched write
		// or external corruption doesn't silently lock the user out of their saved
		// connection — restore it as the live config so we're durable next time too.
		if bak, berr := os.ReadFile(p + ".bak"); berr == nil {
			var bc Config
			if json.Unmarshal(bak, &bc) == nil {
				_ = os.WriteFile(p, bak, 0o600)
				return bc, nil
			}
		}
		return Config{}, fmt.Errorf("client config %s is unreadable (corrupt JSON): %w", p, err)
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
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Keep the current config as last-known-good (only if it parses) BEFORE
	// overwriting, so a later corrupt read can auto-restore from it (see Load).
	// A saved connection profile is a lockout risk if it's ever lost.
	if prev, rerr := os.ReadFile(p); rerr == nil {
		var tmp Config
		if json.Unmarshal(prev, &tmp) == nil {
			_ = os.WriteFile(p+".bak", prev, 0o600)
		}
	}
	// Atomic write: a temp file in the same dir + rename, so a crash or a full
	// disk mid-write can't leave a half-written client.json that locks the user
	// out of their saved connection.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
