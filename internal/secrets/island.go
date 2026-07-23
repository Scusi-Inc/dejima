package secrets

// Per-island secrets: the access tokens an agent's tools need (an EAS token, an
// npm token, a scraper's API key) so they live somewhere managed instead of in
// the repo, a shell profile, or a chat message.
//
// # What this is NOT
//
// It does not hide secrets from agents, and nothing in Dejima should claim it
// does. Every agent in an island runs as the same OS user with a shell, so a
// value usable by a tool in that container is readable by any agent in it. Any
// in-container "hiding" would be obfuscation.
//
// That is not a Dejima shortcoming: Vault, Doppler, Infisical and chamber all
// put the value in the child process's environment, and Dejima's own GitHub
// token is already materialized in-island at /opt/host/gh-config. The industry
// did not solve invisibility — it solved value-if-leaked, via short-lived
// credentials, narrow scope, audit and fast rotation.
//
// So the honest value: out of your repo, out of your chat history, one place to
// see/rotate/revoke, scoped to a single island, deleted with it, and management
// events audited. Hence "secrets manager", never "vault" — the stronger word
// would invite operators to store things they shouldn't.
//
// # Where values live
//
// Values go through the keychain-backed Store above (macOS Keychain or
// libsecret, 0600 file when neither is usable), NOT into a plain JSON file. The
// bookkeeping that isn't sensitive — names, timestamps, who set it, the
// fingerprint — lives in a 0600 meta.json per island, which keeps List cheap
// and keychain-free.
//
// See docs/secrets-manager-spec.md.

import (
	"crypto/sha256"
	"encoding/hex"
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

// Meta is everything about a secret EXCEPT its value. This is what the API, the
// TUI and the SDK see; there is deliberately no field for a value, so no code
// path can serialize one outward by accident.
type Meta struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	SetBy     string    `json:"set_by,omitempty"`

	// Fingerprint is sha256(value)[:8]. Values are never shown after entry —
	// not even a prefix, since leading bytes are often the identifying,
	// high-entropy part and are exactly what leaks into screenshots. A
	// fingerprint leaks nothing and still lets an operator confirm the stored
	// secret matches their copy by hashing it locally.
	Fingerprint string `json:"fingerprint"`

	// RequireApproval is stored from v1 but NOT enforced: per-use approval needs
	// brokered fetch (the agent must ask), which environment injection cannot
	// observe. Persisting it now means that lands without a migration.
	// See docs/secrets-manager-spec.md § Deferred.
	RequireApproval bool `json:"require_approval,omitempty"`
}

// Fingerprint is the first 8 hex characters of the value's SHA-256.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

// ErrNotFound is returned when a named secret isn't in the island's store.
var ErrNotFound = errors.New("secret not found")

// IslandStore manages per-island secrets on top of the keychain-backed Store.
type IslandStore struct {
	kc *Store
}

// OpenIsland builds an IslandStore over the platform keychain (with the file
// fallback the Store already provides).
func OpenIsland() (*IslandStore, error) {
	kc, err := Open()
	if err != nil {
		return nil, err
	}
	return &IslandStore{kc: kc}, nil
}

// Backend names where values are actually kept, for diagnostics.
func (s *IslandStore) Backend() string { return s.kc.Backend() }

// account is the keychain key for one island's secret. Island names are
// validated elsewhere and secret names are restricted to [A-Za-z0-9_], so
// neither can contain the separator.
func account(island, name string) string { return "island:" + island + ":" + name }

// metaMu serializes read-modify-write on the per-island metadata file so a
// concurrent Set and Remove can't clobber each other.
var metaMu sync.Mutex

// metaPath resolves the metadata file WITHOUT creating anything. Reads must not
// have a filesystem side effect: otherwise listing an island with no secrets
// creates a directory for it, and a read after Purge silently resurrects the
// directory that was just deleted.
func metaPath(island string) (string, error) {
	dir, err := paths.IslandSecretsPath(island)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "meta.json"), nil
}

// metaPathForWrite resolves the metadata file and ensures its 0700 directory
// exists — the write path, where creating it is the point.
func metaPathForWrite(island string) (string, error) {
	dir, err := paths.IslandSecretsDir(island)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "meta.json"), nil
}

type metaFile struct {
	Secrets map[string]Meta `json:"secrets,omitempty"`
}

// loadMetaLocked reads the island's metadata. A missing file is an empty set,
// not an error — most islands have no secrets and that isn't a failure.
func loadMetaLocked(island string) (*metaFile, error) {
	p, err := metaPath(island)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &metaFile{Secrets: map[string]Meta{}}, nil
		}
		return nil, err
	}
	var m metaFile
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if m.Secrets == nil {
		m.Secrets = map[string]Meta{}
	}
	return &m, nil
}

// saveMetaLocked writes the metadata atomically at 0600. The temp file is made
// in the same directory so the rename can't cross filesystems, and is 0600 from
// creation — never world-readable, even briefly.
func saveMetaLocked(island string, m *metaFile) error {
	p, err := metaPathForWrite(island)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".meta-*.json")
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
	return os.Rename(tmp.Name(), p)
}

// Set stores or rotates a secret.
//
// Rotation preserves CreatedAt and bumps UpdatedAt, so the TUI can show both
// "added" and "last rotated" — rotation being the actual defence here, whether
// it's happening should be visible.
func (s *IslandStore) Set(island, name, value, setBy string) (Meta, error) {
	if err := ValidateName(name); err != nil {
		return Meta{}, err
	}
	if value == "" {
		return Meta{}, errors.New("secret value is empty")
	}

	metaMu.Lock()
	defer metaMu.Unlock()
	mf, err := loadMetaLocked(island)
	if err != nil {
		return Meta{}, err
	}

	// Value first: if the keychain write fails there must be no metadata
	// claiming a secret exists that can't be read back.
	if err := s.kc.Set(account(island, name), value); err != nil {
		return Meta{}, fmt.Errorf("store secret value: %w", err)
	}

	now := time.Now().UTC()
	m := Meta{
		Name: name, CreatedAt: now, UpdatedAt: now,
		SetBy: setBy, Fingerprint: Fingerprint(value),
	}
	if prev, ok := mf.Secrets[name]; ok {
		m.CreatedAt = prev.CreatedAt // a rotation is not a new secret
		m.RequireApproval = prev.RequireApproval
	}
	mf.Secrets[name] = m
	if err := saveMetaLocked(island, mf); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// Remove deletes a secret. Removing one that isn't there is not an error —
// teardown paths shouldn't have to check first.
func (s *IslandStore) Remove(island, name string) error {
	metaMu.Lock()
	defer metaMu.Unlock()
	mf, err := loadMetaLocked(island)
	if err != nil {
		return err
	}
	// Delete the value even when metadata has no record, so a half-written
	// earlier state can't strand a value in the keychain forever.
	_ = s.kc.Delete(account(island, name))
	if _, ok := mf.Secrets[name]; !ok {
		return nil
	}
	delete(mf.Secrets, name)
	return saveMetaLocked(island, mf)
}

// List returns the island's secrets as metadata, name-sorted. Values are not
// included and cannot be — Meta has no field for one.
func (s *IslandStore) List(island string) ([]Meta, error) {
	metaMu.Lock()
	defer metaMu.Unlock()
	mf, err := loadMetaLocked(island)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(mf.Secrets))
	for _, m := range mf.Secrets {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Names returns the island's secret names, sorted. Safe for an in-island caller:
// knowing WHICH secrets exist is useful to an agent and reveals nothing the
// environment wouldn't already.
func (s *IslandStore) Names(island string) ([]string, error) {
	metas, err := s.List(island)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(metas))
	for _, m := range metas {
		names = append(names, m.Name)
	}
	return names, nil
}

// Get returns a secret's VALUE. Daemon-internal only — this is what
// materialization uses. It must never be reachable from an API handler; the
// value has no route outward by design.
func (s *IslandStore) Get(island, name string) (string, error) {
	v, ok, err := s.kc.Get(account(island, name))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return v, nil
}

// Values returns every name/value pair for materialization. Daemon-internal,
// same rule as Get.
//
// A name present in metadata whose value can't be read is SKIPPED rather than
// failing the whole set: on a --system daemon the keychain may still be locked
// at boot, and one unreadable secret shouldn't stop an island from starting
// with the others.
func (s *IslandStore) Values(island string) (map[string]string, error) {
	metas, err := s.List(island)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(metas))
	for _, m := range metas {
		v, ok, err := s.kc.Get(account(island, m.Name))
		if err != nil || !ok {
			continue
		}
		out[m.Name] = v
	}
	return out, nil
}

// Purge removes an island's secrets — values and metadata — so they never
// outlive the island they were scoped to.
func (s *IslandStore) Purge(island string) error {
	metaMu.Lock()
	defer metaMu.Unlock()
	mf, err := loadMetaLocked(island)
	if err == nil {
		for name := range mf.Secrets {
			_ = s.kc.Delete(account(island, name))
		}
	}
	p, perr := paths.IslandSecretsPath(island)
	if perr != nil {
		return perr
	}
	return os.RemoveAll(p)
}

// minRedactLen is the shortest value worth masking in logs. Masking a
// 4-character value would redact ordinary words throughout the output and make
// logs useless, while protecting something that was never really a secret.
const minRedactLen = 8

// Redact replaces this island's stored values in text with a named placeholder.
// Used to mask secrets in `dejima logs`, which is the likeliest real leak — a
// tool echoing its own configuration.
func (s *IslandStore) Redact(island, text string) string {
	vals, err := s.Values(island)
	if err != nil || len(vals) == 0 {
		return text
	}
	names := make([]string, 0, len(vals))
	for name, v := range vals {
		if len(v) < minRedactLen {
			continue
		}
		names = append(names, name)
	}
	// Longest value first, so a value containing another is masked whole rather
	// than leaving a partially-redacted tail.
	sort.Slice(names, func(i, j int) bool { return len(vals[names[i]]) > len(vals[names[j]]) })
	for _, name := range names {
		text = strings.ReplaceAll(text, vals[name], "[redacted:"+name+"]")
	}
	return text
}
