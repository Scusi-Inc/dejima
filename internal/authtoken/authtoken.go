// Package authtoken issues, stores, and resolves the operator bearer tokens that
// carry the Dejima team-auth model: three built-in roles (owner / operator /
// viewer) and an optional per-island scope. A token is presented on the
// operator API surface (the unix socket and the tailnet-pinned TCP listener) via
// an "Authorization: Bearer <token>" header — it *complements* the tailnet
// identity, it does not replace it: a request with no token is still the
// fully-trusted caller, while a request that carries one is attenuated to that
// token's role + island scope.
//
// This is the exchange-down boundary for the layers *above* the runtime (Scusi,
// a Slack bot, any control plane): the human authenticates with full authority,
// then mints a strictly-narrower service token to hand to an automated caller —
// never its own credential (docs/security-boundary.md). The roles only ever
// *decrease* authority; there is no token that grants more than the trusted
// listener already does.
//
// These tokens are distinct from the per-island tokens in internal/porttoken:
// porttoken authorizes an in-island *brain* on the host-internal autonomy
// listener (exchange-down into a container); authtoken authorizes an *operator*
// client on the control-plane listeners. The two stores never cross — a token
// from one is meaningless to the other's middleware.
//
// At rest (~/.dejima/tokens.json, 0600) only token *metadata* and the SHA-256 of
// each secret are kept; the raw bearer is shown exactly once, at creation. A
// leaked store therefore can't be replayed.
package authtoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// Role is one of the three built-in roles. Authority decreases owner →
// operator → viewer; the middleware (internal/api) maps each route to the
// minimum role that may reach it.
type Role string

const (
	// RoleOwner is full authority — the same surface a trusted (no-token) caller
	// has, including destructive ops (purge), credential management, and token
	// administration. Mint sparingly.
	RoleOwner Role = "owner"
	// RoleOperator may drive island lifecycle (create, hibernate/wake, reset,
	// upgrade, clone, exec, attach, Port/capability grants) but NOT purge an
	// island, manage credentials/tokens, or touch daemon administration.
	RoleOperator Role = "operator"
	// RoleViewer is read + observe only: list/status/overview/logs/events/audit.
	// No mutation, no interactive attach.
	RoleViewer Role = "viewer"
)

// ValidRole reports whether r is one of the three built-in roles.
func ValidRole(r Role) bool {
	switch r {
	case RoleOwner, RoleOperator, RoleViewer:
		return true
	default:
		return false
	}
}

// Token is the stored metadata for one issued bearer token. The secret itself is
// never persisted — only Hash (its SHA-256) — so the store can list and revoke
// tokens without ever holding a replayable credential.
type Token struct {
	ID        string    `json:"id"`                // short public handle; revoke by this
	Label     string    `json:"label,omitempty"`   // human note ("scusi-prod", "phone")
	Role      Role      `json:"role"`              // owner | operator | viewer
	Islands   []string  `json:"islands,omitempty"` // scope; empty = every island
	Hash      string    `json:"hash"`              // sha-256 hex of the secret
	CreatedAt time.Time `json:"created_at"`
}

// Identity is the resolved who/role for an authenticated request. The API layer
// puts it on the request context so handlers — and Lane 1's audit log — can
// attribute and authorize. The zero Identity is meaningless; callers get one
// only from Resolve or by constructing the trusted-owner default explicitly.
type Identity struct {
	// TokenID is the issuing token's id, or "" for the trusted no-token caller.
	TokenID string
	// Subject is a short human label for the actor (the token label, its id, or
	// "local" for the trusted caller) — what an audit record names.
	Subject string
	Role    Role
	// Islands is the token's island scope; empty means unrestricted (all islands).
	Islands []string
}

// Scoped reports whether this identity is limited to a subset of islands.
func (i Identity) Scoped() bool { return len(i.Islands) > 0 }

// MayTouch reports whether this identity's island scope permits acting on the
// named island. An unscoped identity (the common case) may touch any island.
func (i Identity) MayTouch(island string) bool {
	if len(i.Islands) == 0 {
		return true
	}
	for _, s := range i.Islands {
		if s == island {
			return true
		}
	}
	return false
}

// mu serializes read-modify-write of the single store file within this process
// (one daemon). Combined with atomic replace it keeps concurrent token creates
// from clobbering each other.
var mu sync.Mutex

// store is the on-disk JSON shape.
type store struct {
	Tokens []Token `json:"tokens"`
}

func load() (*store, error) {
	p, err := paths.AuthTokensPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &store{}, nil
		}
		return nil, err
	}
	var s store
	if len(b) == 0 {
		return &store{}, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &s, nil
}

func (s *store) save() error {
	p, err := paths.AuthTokensPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replace so a crash mid-write can't truncate the store. The temp
	// file is created 0600 in the same dir (same filesystem → rename is atomic).
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Create mints a fresh token: a 256-bit random secret returned to the caller
// exactly once, plus persisted metadata (role, scope, the secret's hash). The
// raw secret is never stored. islands scopes the token to those island names
// (empty = unrestricted). Returns the secret and the stored Token.
func Create(label string, role Role, islands []string) (secret string, tok Token, err error) {
	if !ValidRole(role) {
		return "", Token{}, fmt.Errorf("invalid role %q (want owner, operator, or viewer)", role)
	}
	scope, err := normalizeIslands(islands)
	if err != nil {
		return "", Token{}, err
	}
	secret, err = mintSecret()
	if err != nil {
		return "", Token{}, err
	}

	mu.Lock()
	defer mu.Unlock()
	s, err := load()
	if err != nil {
		return "", Token{}, err
	}
	tok = Token{
		ID:        s.uniqueID(),
		Label:     strings.TrimSpace(label),
		Role:      role,
		Islands:   scope,
		Hash:      hashSecret(secret),
		CreatedAt: time.Now().UTC(),
	}
	s.Tokens = append(s.Tokens, tok)
	if err := s.save(); err != nil {
		return "", Token{}, err
	}
	return secret, tok, nil
}

// List returns every issued token's metadata (never a secret), newest first.
func List() ([]Token, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]Token, len(s.Tokens))
	copy(out, s.Tokens)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Revoke deletes a token by id. Returns ok=false if no such token existed.
func Revoke(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("token id is required")
	}
	mu.Lock()
	defer mu.Unlock()
	s, err := load()
	if err != nil {
		return false, err
	}
	kept := s.Tokens[:0]
	removed := false
	for _, t := range s.Tokens {
		if t.ID == id {
			removed = true
			continue
		}
		kept = append(kept, t)
	}
	if !removed {
		return false, nil
	}
	s.Tokens = kept
	if err := s.save(); err != nil {
		return false, err
	}
	return true, nil
}

// Resolve maps a presented bearer secret to its Identity. It hashes the input
// and compares against every stored hash in constant time (no early-out timing
// leak of which token, or how many, exist). ok=false for an empty or unknown
// token. A present-but-unknown token must be treated as an authentication
// failure by the caller — never as the trusted no-token caller.
func Resolve(secret string) (Identity, bool) {
	if secret == "" {
		return Identity{}, false
	}
	want := hashSecret(secret)
	mu.Lock()
	defer mu.Unlock()
	s, err := load()
	if err != nil {
		return Identity{}, false
	}
	// Compare against all tokens without short-circuiting: walk the whole slice
	// and keep the match (if any), so the work is independent of the secret.
	var match *Token
	for i := range s.Tokens {
		if subtle.ConstantTimeCompare([]byte(s.Tokens[i].Hash), []byte(want)) == 1 {
			match = &s.Tokens[i]
		}
	}
	if match == nil {
		return Identity{}, false
	}
	return Identity{
		TokenID: match.ID,
		Subject: subjectOf(*match),
		Role:    match.Role,
		Islands: append([]string(nil), match.Islands...),
	}, true
}

// subjectOf is the human label an audit record names for a token: its label if
// set, otherwise "token:<id>".
func subjectOf(t Token) string {
	if t.Label != "" {
		return t.Label
	}
	return "token:" + t.ID
}

// uniqueID returns a short random id not already used by a stored token.
func (s *store) uniqueID() string {
	used := make(map[string]bool, len(s.Tokens))
	for _, t := range s.Tokens {
		used[t.ID] = true
	}
	for {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			// rand should not fail; fall back to a time-seeded id rather than panic.
			return hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000000")))
		}
		id := hex.EncodeToString(b)
		if !used[id] {
			return id
		}
	}
}

// normalizeIslands validates and de-dupes a scope list. Each name must be a
// valid island name (the same charset Load accepts) so a scope entry can never
// smuggle a path separator or traversal. Order is preserved; duplicates dropped.
// An island in the scope need not exist yet — a token may be minted ahead of the
// island it will manage.
func normalizeIslands(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(in))
	var out []string
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if err := project.ValidateName(name); err != nil {
			return nil, fmt.Errorf("invalid island in scope %q: %w", raw, err)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func mintSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
