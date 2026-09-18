// Package agentcreds locates Claude Code credentials on a host so the daemon
// can seed islands with them (and the CLI can push them to a remote daemon).
//
// Claude Code stores its OAuth blob in two places depending on OS:
//   - macOS: the login Keychain, generic password "Claude Code-credentials"
//   - Linux: ~/.claude/.credentials.json
//
// The bind-mount seeding in the daemon only ever saw the file, which is why
// islands hosted on macOS started unauthenticated even when the host itself
// was logged in.
package agentcreds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
)

// Source identifies where credentials were found.
type Source string

const (
	SourceKeychain Source = "keychain"
	SourceFile     Source = "file"

	keychainService = "Claude Code-credentials"
)

// ErrNotFound means no credential source is available on this host.
var ErrNotFound = errors.New("no Claude Code credentials found (run `claude` and log in, or `dejima auth push` from a logged-in machine)")

// LoadClaude returns the Claude Code credentials JSON from the freshest local
// source: the macOS Keychain on darwin, falling back to ~/.claude/.credentials.json.
func LoadClaude() ([]byte, Source, error) {
	if goruntime.GOOS == "darwin" {
		if blob, err := readKeychain(); err == nil {
			return blob, SourceKeychain, nil
		}
	}
	blob, err := readCredentialsFile()
	if err != nil {
		return nil, "", ErrNotFound
	}
	return blob, SourceFile, nil
}

func readKeychain() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		return nil, fmt.Errorf("read keychain item %q: %w", keychainService, err)
	}
	blob := bytes.TrimSpace(out)
	if err := ValidateClaude(blob); err != nil {
		return nil, err
	}
	return blob, nil
}

func readCredentialsFile() ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	blob, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return nil, err
	}
	if err := ValidateClaude(blob); err != nil {
		return nil, err
	}
	return blob, nil
}

// ValidateClaude checks that blob looks like a Claude Code credentials file:
// a JSON object with a claudeAiOauth key. Guards against pushing or seeding
// garbage that would wedge every new island's login.
func ValidateClaude(blob []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		return fmt.Errorf("credentials are not valid JSON: %w", err)
	}
	if _, ok := m["claudeAiOauth"]; !ok {
		return errors.New(`credentials JSON has no "claudeAiOauth" key`)
	}
	return nil
}

// WriteSeed persists blob as the seed credentials file (0600) inside dir,
// creating dir (0700) if needed. Returns the file path.
func WriteSeed(dir string, blob []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".credentials.json")
	// Write via a temp file + rename so a concurrently starting island never
	// sees a partial credentials file through the bind mount.
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// --- Codex ------------------------------------------------------------------
//
// Codex keeps its OAuth blob in a plain file, ~/.codex/auth.json — no keychain
// on any platform, which makes this the simple half of what LoadClaude does.
//
// It exists because the island shim ALREADY copies /opt/host/codex/auth.json
// into the agent's ~/.codex, and credentialBindMounts ALREADY mounts the host's
// ~/.codex there. So a dejima-level codex account works — as long as the login
// is on the DAEMON HOST. An operator logged in on the laptop they are typing at,
// driving a daemon on a Mac mini, has no path at all: the mini's ~/.codex is
// empty and nothing can fill it remotely. `dejima auth push` solved exactly that
// for Claude and did not cover Codex.

// CodexAuthFile is the credential file's name, in the host dir and in the seed
// dir alike. The name matters: the island shim copies it BY NAME, so a seed
// written under any other name is invisible to every island already built.
const CodexAuthFile = "auth.json"

// ErrCodexNotFound means this machine has no Codex login to push.
var ErrCodexNotFound = errors.New(
	"no Codex credentials found (run `codex` and log in on this machine first)")

// LoadCodex returns this host's Codex auth blob.
func LoadCodex() ([]byte, Source, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return nil, "", ErrCodexNotFound
	}
	blob, err := os.ReadFile(filepath.Join(dir, ".codex", CodexAuthFile))
	if err != nil {
		return nil, "", ErrCodexNotFound
	}
	if err := ValidateCodex(blob); err != nil {
		return nil, "", err
	}
	return blob, SourceFile, nil
}

// ValidateCodex rejects a blob that is not the credential file, so a push
// cannot store a truncated read or an unrelated file and have every island
// silently inherit it.
func ValidateCodex(blob []byte) error {
	if len(bytes.TrimSpace(blob)) == 0 {
		return errors.New("codex credentials are empty")
	}
	var probe map[string]any
	if err := json.Unmarshal(blob, &probe); err != nil {
		return fmt.Errorf("codex credentials are not JSON: %w", err)
	}
	if len(probe) == 0 {
		return errors.New("codex credentials are an empty JSON object")
	}
	return nil
}

// WriteCodexSeed stores a pushed Codex blob in the daemon's seed dir, as
// auth.json, atomically — an island starting mid-write must never read half a
// credential through the bind mount.
func WriteCodexSeed(dir string, blob []byte) (string, error) {
	if err := ValidateCodex(blob); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, CodexAuthFile)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
