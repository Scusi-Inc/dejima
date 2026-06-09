// Package clientcfg persists client-side settings (things the CLI/TUI need that
// the daemon doesn't), stored at ~/.dejima/client.json. It is intentionally
// separate from per-island project configs and from daemon state.
package clientcfg

import (
	"encoding/json"
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
