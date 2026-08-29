package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

func ghHosts(t *testing.T, island string) string {
	t.Helper()
	dir, err := paths.GitHubIslandConfigPath(island)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return "<ABSENT>"
		}
		t.Fatalf("read island hosts.yml: %v", err)
	}
	return string(b)
}

// twoIdentities seeds the exact shape of the incident: two identities for the
// same login, and an island pinned to the one whose token is dead.
func twoIdentities(t *testing.T, h http.Handler) {
	t.Helper()
	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/aoos",
		`{"login":"aoos","id":1,"token":"DEAD-TOKEN","default":true}`); rr.Code != http.StatusOK {
		t.Fatalf("seed aoos: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/github",
		`{"login":"aoos","id":1,"token":"LIVE-TOKEN"}`); rr.Code != http.StatusOK {
		t.Fatalf("seed github: %d %s", rr.Code, rr.Body.String())
	}
	p := &project.Project{Name: "krieg", DesiredState: project.StateRunning, GitHubIdentity: "aoos"}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := project.Load("krieg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := islandGHConfigDir(loaded); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !strings.Contains(ghHosts(t, "krieg"), "DEAD-TOKEN") {
		t.Fatalf("the island did not start on the dead token, so nothing below proves anything:\n%s",
			ghHosts(t, "krieg"))
	}
}

// Repointing must move the CREDENTIAL, not only the pin.
//
// Writing p.GitHubIdentity alone would report success while the island kept
// materializing the previous identity's token until something recreated the
// container. That is the same shape as the four staleness bugs: a surface
// asserting a change that the thing underneath has not made.
func TestRepointMovesTheCredentialNotJustThePin(t *testing.T) {
	h, _ := newTestServer(t)
	twoIdentities(t, h)

	rr := do(t, h, http.MethodPut, "/v1/islands/krieg/github-identity", `{"identity":"github"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("repoint: %d %s", rr.Code, rr.Body.String())
	}
	got := ghHosts(t, "krieg")
	if strings.Contains(got, "DEAD-TOKEN") {
		t.Errorf("the island is STILL holding the old identity's token — the pin moved "+
			"and the credential did not:\n%s", got)
	}
	if !strings.Contains(got, "LIVE-TOKEN") {
		t.Errorf("the island did not receive the new identity's token:\n%s", got)
	}
	// The commit-author config is derived from the same identity; refreshing one
	// half makes an island push as one account and commit as another.
	dir, err := paths.GitHubIslandConfigPath("krieg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gitconfig")); err != nil {
		t.Errorf("the commit-author config was not materialized on repoint: %v", err)
	}
	// And the pin itself persisted, so a later recreate agrees with the mount.
	p, err := project.Load("krieg")
	if err != nil {
		t.Fatal(err)
	}
	if p.GitHubIdentity != "github" {
		t.Errorf("pin = %q, want %q — the credential moved but a recreate would move it back",
			p.GitHubIdentity, "github")
	}
}

// A pin the daemon cannot resolve must be REFUSED, not written.
//
// A dangling pin materializes no credential at all, which from inside the island
// is indistinguishable from an expired token — same failure text, different fix.
// Accepting it would turn one typo into an hour.
func TestRepointRefusesAnIdentityThatDoesNotExist(t *testing.T) {
	h, _ := newTestServer(t)
	twoIdentities(t, h)

	rr := do(t, h, http.MethodPut, "/v1/islands/krieg/github-identity", `{"identity":"typo"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("repoint to a missing identity: %d %s, want 404", rr.Code, rr.Body.String())
	}
	p, err := project.Load("krieg")
	if err != nil {
		t.Fatal(err)
	}
	if p.GitHubIdentity != "aoos" {
		t.Errorf("a REFUSED repoint still wrote the pin (%q) — the island would lose its "+
			"credential on the next recreate, long after the error was forgotten", p.GitHubIdentity)
	}
	if !strings.Contains(ghHosts(t, "krieg"), "DEAD-TOKEN") {
		t.Error("a refused repoint disturbed the existing credential")
	}
}

// Usage is attributed the way the DAEMON resolves it, not by reading raw pins.
//
// An island with a blank pin genuinely uses the default and must count against
// it — attributing by pin would report the default as unused on a host where
// every island legitimately follows it, which is the same wrong conclusion the
// old listing produced from the opposite direction.
func TestIdentityUsageCountsIslandsThatFollowTheDefault(t *testing.T) {
	h, _ := newTestServer(t)
	twoIdentities(t, h) // "krieg" pins aoos; aoos is the default
	if err := (&project.Project{Name: "follower", DesiredState: project.StateRunning}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&project.Project{Name: "broken", DesiredState: project.StateRunning,
		GitHubIdentity: "deleted-long-ago"}).Save(); err != nil {
		t.Fatal(err)
	}

	rr := do(t, h, http.MethodGet, "/v1/credentials/github", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var out GitHubIdentitiesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode identities: %v", err)
	}

	byName := map[string][]string{}
	for _, v := range out.Identities {
		byName[v.Name] = v.Islands
	}
	if got := byName["aoos"]; len(got) != 2 {
		t.Errorf("aoos islands = %v, want both the island that PINS it and the one that "+
			"reaches it as the default", got)
	}
	if got := byName["github"]; len(got) != 0 {
		t.Errorf("github islands = %v, want none — reporting a user it does not have is "+
			"what would send someone to refresh the wrong credential again", got)
	}
	if len(out.Dangling) != 1 || out.Dangling[0].Island != "broken" {
		t.Errorf("dangling = %+v, want the island pinned to a deleted identity — folding "+
			"it into 'unused' hides an island with NO credential at all", out.Dangling)
	}
}
