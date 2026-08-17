package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// seedHostGH creates the host operator's own ~/.config/gh under the test HOME,
// so the fallback mount has something real to point at. Without it the mount is
// skipped for a missing path and an "absent" assertion would pass for the wrong
// reason.
func seedHostGH(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "gh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  oauth_token: ghp_host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ghMountOf returns the /opt/host/gh-config bind mount from the last container
// create, or nil when the island got no GitHub credential at all.
func ghMountOf(t *testing.T, f *fakeRuntime) *runtime.BindMount {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.lastCreate.BindMounts {
		if f.lastCreate.BindMounts[i].ContainerPath == "/opt/host/gh-config" {
			m := f.lastCreate.BindMounts[i]
			return &m
		}
	}
	return nil
}

// The finding this closes: a host-owned island used to inherit the operator's
// own gh login, whose read scope is every private repo on the account. A new
// island must now get NO GitHub credential unless one is granted.
func TestHostIslandGetsNoGHCredentialByDefault(t *testing.T) {
	h, f := newTestServer(t)
	hostGH := seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if m := ghMountOf(t, f); m != nil {
		t.Fatalf("a new host island must not inherit the operator's gh login, got mount %q (host dir %q)", m.HostPath, hostGH)
	}
}

// The opt-in restores it — and only after the container is recreated, since the
// credential is a bind mount.
func TestHostGHCredentialGrantMountsIt(t *testing.T) {
	h, f := newTestServer(t)
	hostGH := seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	// Recreate the container the way `dejima upgrade` does.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/upgrade", ""); rr.Code >= 300 {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}
	m := ghMountOf(t, f)
	if m == nil {
		t.Fatal("after an explicit grant the host gh config must be mounted")
	}
	if m.HostPath != hostGH {
		t.Errorf("mount host path = %q, want the host gh dir %q", m.HostPath, hostGH)
	}
	if !m.ReadOnly {
		t.Error("the host gh credential must be mounted read-only")
	}
}

// Revoking takes it away again.
func TestHostGHCredentialRevokeUnmountsIt(t *testing.T) {
	h, f := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/upgrade", ""); rr.Code >= 300 {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}
	if m := ghMountOf(t, f); m != nil {
		t.Fatalf("after revoke the host gh config must not be mounted, got %q", m.HostPath)
	}
	// Revoking twice is a 404, not a silent success — "there was nothing to
	// revoke" is a different fact when auditing a fleet.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusNotFound {
		t.Errorf("second revoke: got %d, want 404", rr.Code)
	}
}

// A per-island identity still wins over the grant — that path was already
// correct and must not be disturbed by the new gate. Here the island holds BOTH
// a named identity and a host grant; it must get the single-identity config, not
// the operator's account-wide one.
func TestIslandIdentityStillBeatsTheHostGrant(t *testing.T) {
	h, f := newTestServer(t)
	hostGH := seedHostGH(t)

	store := &githubid.Store{}
	store.Put(githubid.Identity{Name: "work", Login: "alockwood", Token: "ghp_work"})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj","github_identity":"work"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/upgrade", ""); rr.Code >= 300 {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}
	m := ghMountOf(t, f)
	if m == nil {
		t.Fatal("an island with a named identity must still get a gh config mount")
	}
	if m.HostPath == hostGH {
		t.Fatal("the named identity must win — the island got the operator's account-wide config instead")
	}
	if !strings.Contains(m.HostPath, filepath.Join("islands", "proj")) {
		t.Errorf("expected the per-island config dir, got %q", m.HostPath)
	}
	data, err := os.ReadFile(filepath.Join(m.HostPath, "hosts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ghp_work") || strings.Contains(string(data), "ghp_host") {
		t.Errorf("island must carry only its own identity:\n%s", data)
	}
}

// The grant is meaningless for a tenant island (they resolve their own
// identity), so asking for it is a clean 400 rather than a grant that silently
// does nothing — or worse, one that hands a tenant the host's account.
func TestHostGHCredentialRefusedForTenantIsland(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	p, err := project.Load("proj")
	if err != nil {
		t.Fatal(err)
	}
	p.Owner = "tenant@example.com"
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("granting to a tenant island: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "own identity") {
		t.Errorf("the refusal should point at the per-island identity path, got: %s", rr.Body.String())
	}
}

// MIGRATION. An island persisted before the grant model existed was relying on
// the silent inheritance; a hard cutover would break its clone/push with no
// error naming the cause. It is grandfathered into an explicit grant — same
// access, now visible and revocable.
func TestExistingIslandIsGrandfathered(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	// Rewind to the pre-grant-model shape: access by inheritance, no decision
	// recorded. This is exactly what an upgrading operator's config.toml holds.
	p, err := project.Load("proj")
	if err != nil {
		t.Fatal(err)
	}
	p.HostGitHubReviewed = false
	p.HostGitHubGrant = nil
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	migrated, err := project.Load("proj")
	if err != nil {
		t.Fatal(err)
	}
	if !migrated.HostGitHubAllowed() {
		t.Fatal("an existing host island must keep its access across the cutover, not lose it silently")
	}
	if !migrated.HostGitHubGrant.Grandfathered {
		t.Error("the migration's grant must be marked Grandfathered so leftover exposure stays enumerable")
	}

	// And it must be visible, not just present: the operator has to be able to
	// find every island still carrying it.
	rr := do(t, h, http.MethodGet, "/v1/islands/proj/github/host-credential", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
	}
	var v HostGitHubCredentialView
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Granted || !v.Grandfathered {
		t.Errorf("view = %+v, want granted+grandfathered", v)
	}
}

// The migration must not fight the operator: once revoked, a later Load must
// not re-grandfather the island. Without the reviewed marker the revoke would
// silently undo itself on the next read.
func TestRevokeSurvivesTheMigration(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	for i := 0; i < 3; i++ {
		p, err := project.Load("proj")
		if err != nil {
			t.Fatal(err)
		}
		if p.HostGitHubAllowed() {
			t.Fatalf("load #%d re-granted a revoked credential — the revoke undid itself", i+1)
		}
	}
}

// A tenant island must NOT be grandfathered: it never had the host credential,
// so the migration handing it one would widen access rather than preserve it.
func TestTenantIslandIsNotGrandfathered(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	p, err := project.Load("proj")
	if err != nil {
		t.Fatal(err)
	}
	p.Owner = "tenant@example.com"
	p.HostGitHubReviewed = false
	p.HostGitHubGrant = nil
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	migrated, err := project.Load("proj")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.HostGitHubAllowed() {
		t.Fatal("the migration must not hand a tenant island the host operator's login")
	}
}

// Grant and revoke are ledgered like every other brokered grant — that parity
// is the point of the change, not a side effect.
func TestHostGHCredentialIsLedgered(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	var sawGrant, sawRevoke bool
	for _, e := range readLedger(t) {
		switch e.Type {
		case "github.host-credential.grant":
			sawGrant = true
		case "github.host-credential.revoke":
			sawRevoke = true
		}
	}
	if !sawGrant || !sawRevoke {
		t.Errorf("grant ledgered = %v, revoke ledgered = %v; both must be", sawGrant, sawRevoke)
	}
}

// The unified grants view must include it, so "what does this island hold" has
// one answer rather than four plus a special case.
func TestGrantsViewIncludesHostGH(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	get := func() IslandGrantsResponse {
		t.Helper()
		rr := do(t, h, http.MethodGet, "/v1/islands/proj/grants", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("grants: %d %s", rr.Code, rr.Body.String())
		}
		var out IslandGrantsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if g := get(); g.HostGitHub.Granted {
		t.Error("a fresh island holds no host gh grant")
	} else if !g.HostGitHub.Eligible {
		t.Error("a host-owned island is eligible for the grant")
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if g := get(); !g.HostGitHub.Granted {
		t.Error("the grants view must report the grant once made")
	}
}

// Re-granting a grandfathered island clears the Grandfathered marker: an
// operator deliberately re-granting has decided, and should stop being reported
// as leftover migration state.
func TestRegrantClearsGrandfathered(t *testing.T) {
	p := &project.Project{Name: "x", Owner: project.HostOwner()}
	p.HostGitHubGrant = &project.HostGitHubGrant{GrantedAt: time.Now(), Grandfathered: true}
	p.GrantHostGitHub("alice", time.Now())
	if p.HostGitHubGrant.Grandfathered {
		t.Error("an explicit re-grant must not stay marked as grandfathered")
	}
	if p.HostGitHubGrant.GrantedBy != "alice" {
		t.Errorf("GrantedBy = %q, want alice", p.HostGitHubGrant.GrantedBy)
	}
}
