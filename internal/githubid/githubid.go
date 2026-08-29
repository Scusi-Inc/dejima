// Package githubid manages the daemon's set of GitHub identities: named
// (login, host, token) triples that islands clone and push as. The daemon is
// the credential owner, so islands work from any client device — including ones
// with no gh of their own. A client that does have gh can seed the store with
// `dejima auth push --github`. Each island selects one identity (or the
// default) at create time; the daemon materializes just that identity into the
// island's gh config (see HostsYAML), never the whole set.
package githubid

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// DefaultHost is the GitHub host assumed when an identity doesn't name one.
const DefaultHost = "github.com"

// Identity is one GitHub login the daemon can act as. Owner scopes it to a tenant
// so a team member can self-serve credentials for their OWN islands without the
// host owner having to hold a token for the member's private repos.
type Identity struct {
	Name  string `json:"name"`         // dejima-local handle: "work", "personal", … (unique per Owner)
	Login string `json:"login"`        // GitHub username
	ID    int64  `json:"id,omitempty"` // GitHub numeric user id (for the canonical noreply commit email); 0 if unknown
	Host  string `json:"host"`         // "github.com" or an enterprise host
	Token string `json:"token"`        // OAuth/PAT token — secret, never returned to clients
	// Owner is the tenant that owns this identity ("" = a legacy/host identity).
	// Server-authoritative: set from the authenticated caller, never client-forged.
	// An identity is only ever materialized into islands of the same Owner (plus
	// host-Shared identities into any island) — the containment invariant.
	Owner string `json:"owner,omitempty"`
	// Shared marks a HOST identity as deliberately usable by every tenant's islands
	// (a team-wide org credential). Only the host owner may set it. Ignored on a
	// non-host identity.
	Shared bool `json:"shared,omitempty"`
	// UpdatedAt is when this identity's TOKEN was last written. It exists because
	// two identities for the same GitHub login are indistinguishable in a listing
	// — same name shape, same login, same host — and the only thing that separates
	// a live one from a month-dead one is when it was last refreshed. An operator
	// spent an incident looking at exactly that pair. Zero for identities that
	// predate this field (migrated legacy entries); render it as unknown, never as
	// the epoch, and never as "just now".
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Scopes is the X-OAuth-Scopes GitHub returned when this token was verified.
	// Empty for a fine-grained token, which sends no such header — see ScopeNote.
	Scopes string `json:"scopes,omitempty"`
}

// Meta is an identity without its token: the safe view to hand back to clients.
type Meta struct {
	Name    string `json:"name"`
	Login   string `json:"login"`
	Host    string `json:"host"`
	Default bool   `json:"default"`
	Owner   string `json:"owner,omitempty"`
	Shared  bool   `json:"shared,omitempty"`
	// UpdatedAt mirrors Identity.UpdatedAt — safe to publish (it is not the
	// token, only when the token was last written) and it is the field that
	// tells two same-login identities apart.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Scopes mirrors Identity.Scopes. Safe to publish: it describes what the token
	// may do, never the token.
	Scopes string `json:"scopes,omitempty"`
}

// Store is the per-daemon identity set. Identities are owner-scoped (a flat list,
// unique by (Owner, Name)). Default is the HOST owner's default name; Defaults
// holds each non-host tenant's default. The legacy Identities map is read on load
// and migrated into Idents (as host/"" identities), then dropped on save.
type Store struct {
	Default    string              `json:"default,omitempty"`    // host owner's default identity name
	Defaults   map[string]string   `json:"defaults,omitempty"`   // tenant owner → default identity name
	Idents     []Identity          `json:"idents,omitempty"`     // owner-scoped identities
	Identities map[string]Identity `json:"identities,omitempty"` // LEGACY map (bare-name keyed); migrated → Idents on load
}

func storePath() (string, error) {
	dir, err := paths.GitHubSecretsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store.json"), nil
}

// mu serializes the store across the daemon's request goroutines so a
// read-modify-write (Update) is atomic and a Load never observes a torn write.
var mu sync.Mutex

// Update runs fn against the store under a process-wide lock and persists the
// result atomically. Use it for every read-modify-write — Put/Remove/SetDefault
// — so concurrent writers can't clobber each other (lost updates).
func Update(fn func(*Store) error) (*Store, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := loadLocked()
	if err != nil {
		return nil, err
	}
	if err := fn(s); err != nil {
		return nil, err
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load reads the store under the lock — a consistent snapshot for read-only use.
// Returns an empty (non-nil) store if none exists yet.
func Load() (*Store, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked()
}

func loadLocked() (*Store, error) {
	p, err := storePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Identities: map[string]Identity{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse github identity store: %w", err)
	}
	s.migrateLegacy()
	return &s, nil
}

// migrateLegacy folds a pre-owner-scoping store (the bare-name Identities map)
// into the owner-scoped Idents list, attributing legacy identities to the host
// ("" owner). Idempotent; runs on every load and is a no-op once migrated.
func (s *Store) migrateLegacy() {
	if len(s.Identities) == 0 {
		s.Identities = nil
		return
	}
	for name, id := range s.Identities {
		id.Name = name // the map key was the source of truth for the name
		id.Owner = ""  // legacy identities are host-owned
		if _, ok := s.find("", id.Name); !ok {
			s.Idents = append(s.Idents, id)
		}
	}
	s.Identities = nil
}

// find returns the index of the (owner, name) identity, or ok=false.
func (s *Store) find(owner, name string) (int, bool) {
	for i := range s.Idents {
		if s.Idents[i].Owner == owner && s.Idents[i].Name == name {
			return i, true
		}
	}
	return -1, false
}

// Save persists the store atomically at 0600 (it holds tokens).
func (s *Store) Save() error {
	mu.Lock()
	defer mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes the store via a temp file + rename so a crash or a
// concurrent reader never sees a half-written file. Caller holds mu.
func (s *Store) saveLocked() error {
	p, err := storePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".store-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless once the rename has consumed it
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
	return os.Rename(tmpName, p)
}

// nameRE bounds identity names: a leading alphanumeric, then alphanumerics and
// . _ - (no NUL, whitespace, or the separator characters composite keys would
// need). Keeps names safe as file/dir components and store keys.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateName rejects an unusable identity name before it's stored.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid identity name %q: use 1–64 chars of letters, digits, . _ - starting with a letter or digit", name)
	}
	return nil
}

// PutOwned adds or updates the identity keyed by (Owner, Name). id.Owner must be
// set by the caller (server-authoritative). The first identity added for an owner
// becomes that owner's default.
func (s *Store) PutOwned(id Identity) {
	if strings.TrimSpace(id.Host) == "" {
		id.Host = DefaultHost
	}
	// Stamp the write unless the caller supplied a time (tests, and any future
	// import that knows the real age). migrateLegacy appends directly rather
	// than coming through here, so pre-existing identities correctly keep a zero
	// time — claiming they were refreshed at migration would be the exact lie
	// this field exists to stop telling.
	if id.UpdatedAt.IsZero() {
		id.UpdatedAt = time.Now()
	}
	if i, ok := s.find(id.Owner, id.Name); ok {
		s.Idents[i] = id
	} else {
		s.Idents = append(s.Idents, id)
	}
	if s.DefaultFor(id.Owner) == "" {
		s.setDefaultForRaw(id.Owner, id.Name)
	}
}

// DeleteOwned removes the (owner, name) identity, repointing that owner's default
// to a remaining identity of theirs (or clearing it).
func (s *Store) DeleteOwned(owner, name string) bool {
	i, ok := s.find(owner, name)
	if !ok {
		return false
	}
	s.Idents = append(s.Idents[:i], s.Idents[i+1:]...)
	if s.DefaultFor(owner) == name {
		s.setDefaultForRaw(owner, "")
		for _, m := range s.ownedNames(owner) {
			s.setDefaultForRaw(owner, m)
			break
		}
	}
	return true
}

// DefaultFor returns owner's default identity name ("" if none). The host owner's
// default lives in Default (legacy-compatible); tenants' in Defaults.
func (s *Store) DefaultFor(owner string) string {
	if owner == "" {
		return s.Default
	}
	return s.Defaults[owner]
}

// setDefaultForRaw sets owner's default without validating existence (callers
// that need validation use SetDefaultFor).
func (s *Store) setDefaultForRaw(owner, name string) {
	if owner == "" {
		s.Default = name
		return
	}
	if s.Defaults == nil {
		s.Defaults = map[string]string{}
	}
	if name == "" {
		delete(s.Defaults, owner)
	} else {
		s.Defaults[owner] = name
	}
}

// SetDefaultFor marks (owner, name) as owner's default. Errors if owner has no
// identity by that name.
func (s *Store) SetDefaultFor(owner, name string) error {
	if _, ok := s.find(owner, name); !ok {
		return fmt.Errorf("no such github identity %q", name)
	}
	s.setDefaultForRaw(owner, name)
	return nil
}

// ownedNames returns the sorted identity names owned by owner.
func (s *Store) ownedNames(owner string) []string {
	var out []string
	for _, id := range s.Idents {
		if id.Owner == owner {
			out = append(out, id.Name)
		}
	}
	sort.Strings(out)
	return out
}

// The HOST tenant is represented as "" throughout the store (legacy identities
// already carry Owner==""); the API layer translates project.HostOwner() → "" at
// the boundary, so githubid needs no notion of the host owner's label.

// ResolveForIsland picks the identity to materialize into an island owned by
// islandOwner ("" = a host island), requesting name (or the owner's default when
// empty). THE CONTAINMENT CHOKEPOINT: a host island may use any host identity; a
// tenant island may use only its OWN identities or a host identity marked Shared.
// An operator's token can never reach another tenant's island.
func (s *Store) ResolveForIsland(islandOwner, name string) (Identity, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		if name = s.defaultName(islandOwner); name == "" {
			return Identity{}, false
		}
	}
	hostIsland := islandOwner == ""
	for _, id := range s.Idents {
		if id.Name != name {
			continue
		}
		switch {
		case hostIsland && id.Owner == "": // host island ↔ any host identity
			return id, true
		case id.Owner == islandOwner: // exact tenant match
			return id, true
		case id.Shared && id.Owner == "": // host-shared → any island
			return id, true
		}
	}
	return Identity{}, false
}

// defaultName resolves the effective default identity NAME for an island owned by
// islandOwner: the owner's own default, else (for a tenant) the host default only
// if it's Shared. A host island uses the host default directly.
func (s *Store) defaultName(islandOwner string) string {
	if d := s.DefaultFor(islandOwner); d != "" {
		return d
	}
	if islandOwner == "" {
		return "" // host island with no host default → nothing
	}
	if hd := s.Default; hd != "" {
		if i, ok := s.find("", hd); ok && s.Idents[i].Shared {
			return hd
		}
	}
	return ""
}

// ListForOwner returns the identity metadata (no tokens) visible to owner ("" =
// host): their own identities, plus host-Shared ones. ownsAll (the host owner)
// sees everything.
func (s *Store) ListForOwner(owner string, ownsAll bool) []Meta {
	out := make([]Meta, 0, len(s.Idents))
	for _, id := range s.Idents {
		visible := ownsAll || id.Owner == owner || (id.Shared && id.Owner == "")
		if !visible {
			continue
		}
		out = append(out, Meta{
			Name: id.Name, Login: id.Login, Host: id.Host,
			Default: id.Name == s.DefaultFor(id.Owner),
			Owner:   id.Owner, Shared: id.Shared,
			UpdatedAt: id.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Find returns the (owner, name) identity (with token) — for handlers that need
// to check existence/ownership before a write.
func (s *Store) Find(owner, name string) (Identity, bool) {
	if i, ok := s.find(owner, name); ok {
		return s.Idents[i], true
	}
	return Identity{}, false
}

// --- back-compat shims: operate on the HOST tenant ("") -------------------
// These preserve the pre-owner-scoping API for host-owner call sites (and tests)
// that manage the daemon's own identities; the owner-aware methods above are the
// path for tenant-scoped operations.

// Put adds/updates a host identity (its Owner is used as-is; the zero value ""
// is the host tenant).
func (s *Store) Put(id Identity) { s.PutOwned(id) }

// Resolve resolves a host identity by name (or the host default when empty).
func (s *Store) Resolve(name string) (Identity, bool) { return s.ResolveForIsland("", name) }

// SetDefault sets the host default identity.
func (s *Store) SetDefault(name string) error { return s.SetDefaultFor("", name) }

// Remove deletes a host identity.
func (s *Store) Remove(name string) bool { return s.DeleteOwned("", name) }

// List returns host (+ host-shared) identity metadata.
func (s *Store) List() []Meta { return s.ListForOwner("", false) }

// HostsYAML renders the gh hosts.yml for a single identity — what gh reads from
// GH_CONFIG_DIR to authenticate git over HTTPS inside an island. Only ever one
// identity per file.
//
// It MUST emit the modern multi-account schema (the per-user `users:` map), not
// just the legacy top-level form. gh migrates a legacy-only hosts.yml to this
// schema on first use, which means writing back to GH_CONFIG_DIR — but the
// daemon mounts that dir read-only, so the migration fails and `gh auth
// setup-git` errors out, leaving the island with no git credential helper (the
// clone then can't authenticate). Materializing the already-migrated form means
// gh has nothing to write. See also ConfigYAML, which supplies the version
// marker gh checks before deciding to migrate.
func HostsYAML(id Identity) string {
	host := strings.TrimSpace(id.Host)
	if host == "" {
		host = DefaultHost
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", host)
	// Modern per-user entry (what gh 2.x keys off for multi-account).
	b.WriteString("    users:\n")
	fmt.Fprintf(&b, "        %s:\n", id.Login)
	fmt.Fprintf(&b, "            oauth_token: %s\n", id.Token)
	// Legacy top-level keys, kept for backward compatibility with older gh.
	fmt.Fprintf(&b, "    oauth_token: %s\n", id.Token)
	fmt.Fprintf(&b, "    user: %s\n", id.Login)
	b.WriteString("    git_protocol: https\n")
	return b.String()
}

// GitAuthor derives the git commit author (name, email) for an island that acts
// as this identity. Without it, commits inherit the host's gitconfig user.* —
// so a push authenticated as "work" gets authored with whatever email the
// daemon host happens to have, which GitHub then misattributes (it keys
// attribution off the email). We use GitHub's privacy-preserving noreply email
// so commits attribute to the right account without exposing a real address:
//   - canonical form (preferred): "<id>+<login>@users.noreply.<host>"
//   - fallback when the numeric id is unknown (identity stored before id capture):
//     "<login>@users.noreply.<host>" — still account-linked, just the older form.
func GitAuthor(id Identity) (name, email string) {
	login := strings.TrimSpace(id.Login)
	host := strings.TrimSpace(id.Host)
	if host == "" {
		host = DefaultHost
	}
	domain := "users.noreply." + host
	if id.ID > 0 {
		email = fmt.Sprintf("%d+%s@%s", id.ID, login, domain)
	} else {
		email = fmt.Sprintf("%s@%s", login, domain)
	}
	return login, email
}

// GitConfig renders a minimal gitconfig carrying just the identity's commit
// author. The daemon mounts this at /opt/host/gitconfig for identity-scoped
// islands (in place of the host's own gitconfig), and the entrypoint applies
// user.name/user.email from it — so authorship matches the push credential.
func GitConfig(id Identity) string {
	name, email := GitAuthor(id)
	return fmt.Sprintf("[user]\n\tname = %s\n\temail = %s\n", name, email)
}

// ConfigYAML renders the gh config.yml that accompanies HostsYAML. Its only job
// is to carry the schema version marker: with it present, gh treats the
// materialized config as already-migrated and never tries to write to the
// read-only GH_CONFIG_DIR mount. Without it, gh runs a migration on first use
// and fails on the read-only dir (see HostsYAML).
func ConfigYAML() string {
	return "version: \"1\"\n"
}

// ScopeNote explains, in one line, what a token's scopes mean for the work an
// island does — or says plainly that it cannot tell.
//
// Three states, and collapsing any two of them is how this went wrong:
//
//	""            a FINE-GRAINED token. GitHub sends no X-OAuth-Scopes header for
//	              these; permissions are per-repository and not visible from
//	              /user at all. "Unknown" is the honest answer, NOT "no scopes".
//	has "repo"    a classic token that can clone, push, and open pull requests.
//	otherwise     a classic token that authenticates and cannot write. This is
//	              the state that produced "Resource not accessible by personal
//	              access token" inside an island, hours after `github connect`
//	              reported success — because authenticating and being ABLE TO DO
//	              THE WORK are different questions and only the first was asked.
func ScopeNote(scopes string) (note string, canWrite bool) {
	s := strings.TrimSpace(scopes)
	if s == "" {
		return "fine-grained (per-repo; not introspectable)", true
	}
	for _, f := range strings.Split(s, ",") {
		if strings.TrimSpace(f) == "repo" {
			return s, true
		}
	}
	return s + "  ⚠ no `repo` scope", false
}
