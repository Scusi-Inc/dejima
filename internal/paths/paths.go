// Package paths centralizes the on-host filesystem layout for Dejima.
package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Root returns ~/.dejima, creating it if necessary.
func Root() (string, error) {
	home, err := userHome()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	root := filepath.Join(home, ".dejima")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", root, err)
	}
	return root, nil
}

// userHome resolves the invoking user's home directory. Under sudo (running as
// root with SUDO_USER set) it returns the *real* user's home, not /var/root:
// Dejima's per-user state (config, ledger, tokens, the daemon install-meta)
// belongs to the human who ran the command, and a privileged subcommand like
// `dejima service install --system` must not strand that state in root's home
// where the daemon — which runs as the user — can never read it. Off Unix and
// for a genuine root login (no SUDO_USER) it falls back to os.UserHomeDir.
func userHome() (string, error) {
	if os.Geteuid() == 0 {
		if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
			if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
				return u.HomeDir, nil
			}
		}
	}
	return os.UserHomeDir()
}

// CapabilitiesDir returns ~/.dejima/capabilities, creating it (0700). It holds
// the user-authored executables the capability broker's script adapter runs
// (docs/capability-broker-spec.md §2.2). Owned by the daemon user and not
// island-writable — that is the curation boundary.
func CapabilitiesDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "capabilities")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
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

// PanicFlagPath returns ~/.dejima/PANIC — the presence of this file stops the
// daemon from auto-starting (adopting) any island at startup. Written by
// `dejima panic`, removed by `dejima panic --clear`.
func PanicFlagPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "PANIC"), nil
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

// AuthTokensPath returns ~/.dejima/tokens.json — the store of issued operator
// bearer tokens (owner/operator/viewer roles + optional per-island scope) for
// the team-auth path. Only token *metadata* and a SHA-256 of each secret are
// kept here (never the raw bearer), so a leaked file can't be replayed. 0600,
// daemon-host-only, outside every container's blast radius.
func AuthTokensPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tokens.json"), nil
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

// DaemonInstallMetaPath returns ~/.dejima/daemon-install.json — install context
// recorded by `dejima service install` (the source checkout dir and whether it's
// a system service) so the daemon can update + restart itself correctly.
func DaemonInstallMetaPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "daemon-install.json"), nil
}

// AccountAuthorizedKeysPath returns ~/.dejima/ssh_authorized_keys — the
// account-wide allow-set of public keys that may ssh into *every* island
// (current and future). Consulted in addition to each island's own
// authorized_keys, so registering a key once grants fleet-wide access without
// per-island seeding. 0600.
func AccountAuthorizedKeysPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ssh_authorized_keys"), nil
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

// LinksDir returns ~/.dejima/links — the daemon's store of inter-island link
// grants (Lane 5, Phase 2). Created 0700.
func LinksDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "links")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LLMSecretsDir returns ~/.dejima/secrets/llm — the daemon's store of LLM
// provider credentials (provider→api_key) and the per-island provider configs
// materialized from them. Created 0700.
func LLMSecretsDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "secrets", "llm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LLMIslandConfigPath returns the per-island LLM config dir path WITHOUT
// creating it — for cleanup when an island is torn down (it holds plaintext
// provider keys, so it lives under secrets/ and must be removed on teardown).
func LLMIslandConfigPath(name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "secrets", "llm", "islands", name), nil
}

// LLMIslandConfigDir returns the per-island LLM config dir the daemon
// materializes (containing one <provider>.env per referenced provider) and
// mounts read-only at /opt/host/llm. Created 0700.
func LLMIslandConfigDir(name string) (string, error) {
	dir, err := LLMIslandConfigPath(name)
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
