package authtoken

import (
	"os"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/paths"
)

func TestCreateResolveRevoke(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	secret, tok, err := Create("scusi-prod", RoleOperator, []string{"alpha", "beta"}, "amanda")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tok.Owner != "amanda" {
		t.Fatalf("owner should be persisted on the token, got %q", tok.Owner)
	}
	if secret == "" || len(secret) != 64 {
		t.Fatalf("secret should be 64 hex chars, got %d", len(secret))
	}
	if tok.ID == "" || tok.Role != RoleOperator {
		t.Fatalf("unexpected token meta: %+v", tok)
	}
	if tok.Hash == secret {
		t.Fatal("stored hash must not equal the secret")
	}

	id, ok := Resolve(secret)
	if !ok {
		t.Fatal("Resolve(secret) failed for a freshly minted token")
	}
	if id.Role != RoleOperator || id.TokenID != tok.ID {
		t.Fatalf("resolved identity mismatch: %+v", id)
	}
	if id.Owner != "amanda" || id.OwnsAll() {
		t.Fatalf("resolved identity owner/ownsall wrong: owner=%q ownsAll=%v", id.Owner, id.OwnsAll())
	}
	if !id.Scoped() || id.MayTouch("gamma") {
		t.Fatalf("scope not enforced: scoped=%v mayTouchGamma=%v", id.Scoped(), id.MayTouch("gamma"))
	}
	if !id.MayTouch("alpha") || !id.MayTouch("beta") {
		t.Fatal("scoped identity should permit its in-scope islands")
	}
	if id.Subject != "scusi-prod" {
		t.Fatalf("subject should be the label, got %q", id.Subject)
	}

	// Unknown / empty secrets never resolve.
	if _, ok := Resolve("deadbeef"); ok {
		t.Error("unknown secret resolved")
	}
	if _, ok := Resolve(""); ok {
		t.Error("empty secret resolved")
	}

	// Revoke and confirm the secret stops resolving.
	removed, err := Revoke(tok.ID)
	if err != nil || !removed {
		t.Fatalf("Revoke: removed=%v err=%v", removed, err)
	}
	if _, ok := Resolve(secret); ok {
		t.Error("revoked secret still resolves")
	}
	if again, _ := Revoke(tok.ID); again {
		t.Error("double-revoke reported a second removal")
	}
}

func TestSecretNotStoredInClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret, _, err := Create("phone", RoleViewer, nil, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p, err := paths.AuthTokensPath()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatal("raw secret was persisted to the store file")
	}
	// File perms must be owner-only.
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store perms = %o, want 600", perm)
	}
}

func TestUnscopedTokenTouchesAnyIsland(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret, _, err := Create("", RoleOwner, nil, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id, ok := Resolve(secret)
	if !ok {
		t.Fatal("resolve")
	}
	if id.Scoped() {
		t.Fatal("a nil scope must be unscoped")
	}
	if !id.MayTouch("anything") {
		t.Fatal("unscoped token should touch any island")
	}
	// An unlabeled token is named by its id.
	if !strings.HasPrefix(id.Subject, "token:") {
		t.Errorf("subject = %q, want token:<id>", id.Subject)
	}
}

func TestCreateRejectsBadRoleAndScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, err := Create("x", Role("admin"), nil, ""); err == nil {
		t.Error("Create accepted an invalid role")
	}
	// A scope entry with a path separator must be rejected (it could otherwise
	// smuggle traversal into an authorization comparison).
	if _, _, err := Create("x", RoleViewer, []string{"../etc"}, ""); err == nil {
		t.Error("Create accepted an invalid island in scope")
	}
}

func TestListNewestFirstAndScopeDedup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, err := Create("a", RoleViewer, []string{"x", "x", " y "}, ""); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, _, err := Create("b", RoleOwner, nil, ""); err != nil {
		t.Fatalf("create b: %v", err)
	}
	toks, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(toks) != 2 {
		t.Fatalf("want 2 tokens, got %d", len(toks))
	}
	if toks[0].Label != "b" {
		t.Errorf("List not newest-first: %q first", toks[0].Label)
	}
	// Find token a and confirm its scope was de-duped + trimmed.
	for _, tk := range toks {
		if tk.Label == "a" {
			if len(tk.Islands) != 2 || tk.Islands[0] != "x" || tk.Islands[1] != "y" {
				t.Errorf("scope not normalized: %v", tk.Islands)
			}
		}
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range []Role{RoleOwner, RoleOperator, RoleViewer} {
		if !ValidRole(r) {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range []Role{"", "admin", "Owner"} {
		if ValidRole(r) {
			t.Errorf("%q should be invalid", r)
		}
	}
}
