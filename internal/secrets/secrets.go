// Package secrets stores small dejima-held secrets (webhook HMAC keys and the
// like) at rest in the OS keychain when one is available — the macOS login
// Keychain via `security`, or the Secret Service (libsecret) via `secret-tool`
// on Linux — and falls back to a 0600 file under ~/.dejima/secrets otherwise.
//
// The fallback is deliberate, not a failure mode: a `--system` daemon starts at
// boot *before any login*, when the login keychain is still locked, so a
// keychain write/read can fail until someone logs in once. Rather than hard-fail
// (the trap the roadmap calls out — reporting "never configured" when the
// keychain is merely locked), every operation degrades to the file store. So a
// secret set while the keychain was locked lands in the file and is still
// readable; secrets set while unlocked live in the keychain.
package secrets

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/aoos/dejima/internal/paths"
)

// service is the keychain "service" (macOS) / attribute (Linux) under which all
// dejima secrets are grouped, so they're easy to find and purge.
const service = "dejima"

// Store reads/writes secrets, preferring the OS keychain and falling back to a
// file. It's safe for concurrent use.
type Store struct {
	kc   backend    // OS keychain backend; nil when none is usable on this host
	file *fileStore // always present
}

// backend is one secret store implementation (a keychain).
type backend interface {
	get(account string) (string, bool, error) // value, found, err (err ⇒ caller falls back)
	set(account, value string) error
	del(account string) error
	name() string
}

// Open builds a Store: the platform keychain backend if its CLI is on PATH, plus
// the always-available file fallback.
func Open() (*Store, error) {
	fs, err := newFileStore()
	if err != nil {
		return nil, err
	}
	return &Store{kc: osKeychain(), file: fs}, nil
}

// Backend names the active keychain backend ("macos-keychain", "secret-service",
// or "file" when no keychain is usable) — for diagnostics.
func (s *Store) Backend() string {
	if s.kc != nil {
		return s.kc.name()
	}
	return "file"
}

// Get returns the secret for account. It prefers the keychain; on a miss, a
// locked keychain, or any keychain error it falls through to the file store, so
// a secret written under either backend is found.
func (s *Store) Get(account string) (string, bool, error) {
	if s.kc != nil {
		if v, ok, err := s.kc.get(account); err == nil && ok {
			return v, true, nil
		}
	}
	return s.file.get(account)
}

// Set stores the secret. It writes the keychain when one is usable (and clears
// any stale file copy so the keychain stays authoritative); if the keychain is
// unavailable or locked, it writes the file fallback instead.
func (s *Store) Set(account, value string) error {
	if s.kc != nil {
		if err := s.kc.set(account, value); err == nil {
			_ = s.file.del(account)
			return nil
		}
	}
	return s.file.set(account, value)
}

// Delete removes the secret from both backends (best-effort on the keychain).
func (s *Store) Delete(account string) error {
	if s.kc != nil {
		_ = s.kc.del(account)
	}
	return s.file.del(account)
}

// osKeychain returns the platform keychain backend when its CLI is present, else
// nil (callers then use the file fallback transparently).
func osKeychain() backend {
	switch goruntime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("security"); err == nil {
			return macKeychain{}
		}
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err == nil {
			return secretService{}
		}
	}
	return nil
}

// --- macOS Keychain (`security`) -------------------------------------------

type macKeychain struct{}

func (macKeychain) name() string { return "macos-keychain" }

func (macKeychain) get(account string) (string, bool, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		// Exit 44 = item not found (a definitive miss); anything else (e.g. 51,
		// keychain locked / user-interaction-not-allowed) is an error so the
		// caller falls back to the file rather than treating it as "absent".
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimRight(string(out), "\n"), true, nil
}

func (macKeychain) set(account, value string) error {
	// -U updates the item if it already exists. The value is passed as an arg
	// (visible only to this same-user process list, like the daemon itself).
	return exec.Command("security", "add-generic-password", "-U",
		"-s", service, "-a", account, "-w", value).Run()
}

func (macKeychain) del(account string) error {
	err := exec.Command("security", "delete-generic-password", "-s", service, "-a", account).Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
		return nil // already gone
	}
	return err
}

// --- Linux Secret Service (`secret-tool` / libsecret) ----------------------

type secretService struct{}

func (secretService) name() string { return "secret-service" }

func (secretService) get(account string) (string, bool, error) {
	out, err := exec.Command("secret-tool", "lookup", "service", service, "account", account).Output()
	if err != nil {
		// secret-tool exits non-zero on a miss AND when the collection is locked;
		// it doesn't distinguish them, so treat any error as "fall back to file".
		return "", false, err
	}
	if len(out) == 0 {
		return "", false, nil
	}
	return string(out), true, nil
}

func (secretService) set(account, value string) error {
	cmd := exec.Command("secret-tool", "store", "--label", "dejima "+account,
		"service", service, "account", account)
	cmd.Stdin = strings.NewReader(value) // secret-tool reads the value from stdin
	return cmd.Run()
}

func (secretService) del(account string) error {
	return exec.Command("secret-tool", "clear", "service", service, "account", account).Run()
}

// --- file fallback ---------------------------------------------------------

// fileStore is a 0600 JSON map of account→secret under ~/.dejima/secrets.
type fileStore struct {
	mu   sync.Mutex
	path string
}

func newFileStore() (*fileStore, error) {
	root, err := paths.Root()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileStore{path: filepath.Join(dir, "keystore.json")}, nil
}

func (f *fileStore) load() (map[string]string, error) {
	b, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (f *fileStore) get(account string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[account]
	return v, ok, nil
}

func (f *fileStore) set(account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[account] = value
	return f.save(m)
}

func (f *fileStore) del(account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return nil
	}
	delete(m, account)
	return f.save(m)
}

// save writes the map atomically (temp file + rename) at 0600.
func (f *fileStore) save(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".keystore-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
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
	return os.Rename(tmp.Name(), f.path)
}
