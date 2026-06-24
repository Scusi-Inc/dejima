// Package hosttmux materializes the tmux config Dejima applies to the host
// terminals it creates (operator shells running in tmux ON the daemon host).
//
// In-container agent sessions get their tmux defaults from the image's
// /etc/tmux.conf (see image/tmux.conf). Host terminals run on the daemon host
// where there is no such system config and where the operator may already keep
// a ~/.tmux.conf we must not clobber. So instead of touching the user's config
// we ship a dedicated config and pass it to ONLY Dejima-created host sessions
// via `tmux -f`; that config first `source-file`s the user's ~/.tmux.conf and
// then layers Dejima's passthrough/extended-keys defaults on top.
package hosttmux

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aoos/dejima/internal/paths"
)

//go:embed tmux-host.conf
var config []byte

// configName is the materialized file under ~/.dejima.
const configName = "tmux-host.conf"

// ConfigPath writes the embedded host-tmux config to ~/.dejima/tmux-host.conf
// (refreshing it if the embedded content changed) and returns the path, for use
// as the argument to `tmux -f`. It is cheap and idempotent: callers invoke it on
// every host session create/attach so a daemon upgrade re-materializes the
// latest config without any install step.
func ConfigPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, configName)
	if cur, err := os.ReadFile(p); err == nil && string(cur) == string(config) {
		return p, nil // already up to date
	}
	if err := os.WriteFile(p, config, 0o644); err != nil {
		return "", fmt.Errorf("write host tmux config: %w", err)
	}
	return p, nil
}

// NewSessionArgs returns the leading `tmux -f <config> …` argv for a host tmux
// command, so host session create/attach share one passthrough-enabled config.
// On failure to materialize the config it falls back to a bare `tmux` so host
// terminals never break just because the config could not be written.
func NewSessionArgs(rest ...string) []string {
	p, err := ConfigPath()
	if err != nil {
		return append([]string{"tmux"}, rest...)
	}
	return append([]string{"tmux", "-f", p}, rest...)
}
