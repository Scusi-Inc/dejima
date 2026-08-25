package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// An island does NOT read the host's ~/.config/gh. It reads a per-island
// hosts.yml the daemon materializes from the identity store — written in
// credentialBindMounts, which runs at container create and nowhere else.
//
// So refreshing an expired token host-side left every existing island holding
// the credential it was created with. `dejima github ls` showed the new token
// while several islands failed with "Bad credentials" against a hosts.yml
// untouched for a month, and nothing connected the two facts. The operator did
// the right thing and it reached nothing.
//
// The mount is a DIRECTORY, so rewriting the file inside it is visible to a
// running container immediately.
func TestIdentityChangeRefreshesIslandCredential(t *testing.T) {
	h, _ := newTestServer(t)
	if err := (&project.Project{Name: "isl", DesiredState: project.StateRunning}).Save(); err != nil {
		t.Fatal(err)
	}

	// Seed an identity and materialize the island's copy, as create would.
	put := `{"login":"aoos","id":1,"token":"OLD-TOKEN","default":true}`
	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/work", put); rr.Code != http.StatusOK {
		t.Fatalf("seed identity: %d %s", rr.Code, rr.Body.String())
	}
	p, err := project.Load("isl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := islandGHConfigDir(p); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	read := func() string {
		dir, err := paths.GitHubIslandConfigPath("isl")
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
		if err != nil {
			t.Fatalf("island hosts.yml unreadable: %v", err)
		}
		return string(b)
	}
	if !strings.Contains(read(), "OLD-TOKEN") {
		t.Fatalf("the island did not get the seeded token, so this test proves nothing:\n%s", read())
	}

	// The operator refreshes the expired token, exactly as `dejima github
	// connect` does.
	rot := `{"login":"aoos","id":1,"token":"NEW-TOKEN","default":true}`
	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/work", rot); rr.Code != http.StatusOK {
		t.Fatalf("rotate identity: %d %s", rr.Code, rr.Body.String())
	}

	got := read()
	if strings.Contains(got, "OLD-TOKEN") {
		t.Errorf("the island still holds the OLD token after the operator refreshed it.\n" +
			"Every island keeps failing with Bad credentials while `dejima github ls` " +
			"shows the new one, and nothing connects the two.")
	}
	if !strings.Contains(got, "NEW-TOKEN") {
		t.Errorf("the island's hosts.yml does not carry the new token:\n%s", got)
	}
}
