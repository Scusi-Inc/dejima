// Package paths centralizes the on-host filesystem layout for Dejima.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Root returns ~/.dejima, creating it if necessary.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	root := filepath.Join(home, ".dejima")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", root, err)
	}
	return root, nil
}

// SocketPath returns the Unix socket the daemon listens on.
func SocketPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "dejimad.sock"), nil
}

// ProjectsDir returns ~/.dejima/projects, creating it if necessary.
func ProjectsDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "projects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// ProjectDir returns ~/.dejima/projects/<name>/, creating it if necessary.
func ProjectDir(name string) (string, error) {
	projects, err := ProjectsDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(projects, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// ProjectConfigPath returns ~/.dejima/projects/<name>/config.toml.
func ProjectConfigPath(name string) (string, error) {
	dir, err := ProjectDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// ClaudeSeedDir returns ~/.dejima/secrets/claude — where the daemon
// materializes Claude credentials (from the host Keychain/file or a
// `dejima auth push`) for read-only mounting into islands. Created 0700.
func ClaudeSeedDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "secrets", "claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// HostGHConfigDir returns the user's ~/.config/gh dir (may not exist).
func HostGHConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gh"), nil
}

// HostClaudeDir returns the user's ~/.claude dir (may not exist).
func HostClaudeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// HostCodexDir returns the user's ~/.codex dir (may not exist).
func HostCodexDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// HostGitConfig returns the user's ~/.gitconfig path (may not exist).
func HostGitConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gitconfig"), nil
}
