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

// LedgerPath returns ~/.dejima/ledger.jsonl — the append-only, hash-chained
// audit log of brokered Port operations (scope grants and file Trades).
func LedgerPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ledger.jsonl"), nil
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

// TokenPath returns ~/.dejima/projects/<name>/token — the per-island bearer
// token for the authenticated in-island → dejimad path (macOS autonomy route).
func TokenPath(name string) (string, error) {
	dir, err := ProjectDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}

// HostKeyPath returns ~/.dejima/ssh_host_ed25519 — the daemon's SSH host key
// for the SSH-façade listener. One key for the daemon (it is the single SSH
// front door for every island), generated on first use, 0600.
func HostKeyPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ssh_host_ed25519"), nil
}

// SSHDir returns ~/.dejima/projects/<name>/ssh — the per-island SSH dir holding
// authorized_keys (the public keys allowed to ssh into this island). Created 0700.
func SSHDir(name string) (string, error) {
	dir, err := ProjectDir(name)
	if err != nil {
		return "", err
	}
	sshDir := filepath.Join(dir, "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", err
	}
	return sshDir, nil
}

// AuthorizedKeysPath returns ~/.dejima/projects/<name>/ssh/authorized_keys —
// the public keys permitted to ssh into this island via the daemon's façade.
func AuthorizedKeysPath(name string) (string, error) {
	dir, err := SSHDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "authorized_keys"), nil
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

// GitHubSecretsDir returns ~/.dejima/secrets/github — the daemon's store of
// GitHub identities and the per-island gh configs materialized from them.
// Created 0700.
func GitHubSecretsDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "secrets", "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// GitHubIslandConfigPath returns the per-island gh config dir path WITHOUT
// creating it — for cleanup when an island is torn down.
func GitHubIslandConfigPath(name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "secrets", "github", "islands", name), nil
}

// GitHubIslandConfigDir returns the per-island gh config dir the daemon
// materializes (containing a single-identity hosts.yml) and mounts read-only at
// /opt/host/gh-config when an island uses a specific GitHub identity. Created 0700.
func GitHubIslandConfigDir(name string) (string, error) {
	dir, err := GitHubIslandConfigPath(name)
	if err != nil {
		return "", err
	}
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
