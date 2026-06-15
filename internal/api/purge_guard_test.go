package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// gitHook returns an execHook that answers the git probes gitStatusOf issues,
// simulating a workspace with the given repo/branch/dirty/ahead state.
func gitHook(isRepo bool, branch string, dirtyFiles, ahead int) func([]string) (string, string, int, bool) {
	return func(cmd []string) (string, string, int, bool) {
		joined := strings.Join(cmd, " ")
		switch {
		case strings.Contains(joined, "rev-parse --git-dir"):
			if isRepo {
				return ".git", "", 0, true
			}
			return "", "not a git repository", 1, true
		case strings.Contains(joined, "rev-parse --abbrev-ref HEAD"):
			return branch, "", 0, true
		case strings.Contains(joined, "status --porcelain"):
			lines := make([]string, dirtyFiles)
			for i := range lines {
				lines[i] = " M file"
			}
			return strings.Join(lines, "\n"), "", 0, true
		case strings.Contains(joined, "@{u}..HEAD"):
			return strconv.Itoa(ahead), "", 0, true
		case strings.Contains(joined, "HEAD..@{u}"):
			return "0", "", 0, true
		}
		return "", "", 0, false
	}
}

// TestPurgeUnpushedWorkGuard covers the data-loss guard on DELETE /v1/islands:
// dirty or unpushed work blocks the purge with 409 unless force=true, a clean
// or non-git workspace purges freely, and a non-running island can't be verified
// so it also requires force.
func TestPurgeUnpushedWorkGuard(t *testing.T) {
	cases := []struct {
		name       string
		status     runtime.ContainerStatus
		isRepo     bool
		dirtyFiles int
		ahead      int
		wantBlock  bool
		wantSubstr string // expected fragment of the 409 message
	}{
		{name: "clean", status: runtime.StatusRunning, isRepo: true, wantBlock: false},
		{name: "not-a-repo", status: runtime.StatusRunning, isRepo: false, wantBlock: false},
		{name: "dirty", status: runtime.StatusRunning, isRepo: true, dirtyFiles: 2, wantBlock: true, wantSubstr: "uncommitted change"},
		{name: "unpushed", status: runtime.StatusRunning, isRepo: true, ahead: 3, wantBlock: true, wantSubstr: "unpushed commit"},
		{name: "dirty-and-unpushed", status: runtime.StatusRunning, isRepo: true, dirtyFiles: 1, ahead: 1, wantBlock: true, wantSubstr: "and"},
		{name: "not-running", status: runtime.StatusStopped, isRepo: true, wantBlock: true, wantSubstr: "not running"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, f := newTestServer(t) // sets HOME to a temp dir
			f.status = tc.status
			f.execHook = gitHook(tc.isRepo, "feature", tc.dirtyFiles, tc.ahead)

			save := func() {
				if err := (&project.Project{Name: "isl"}).Save(); err != nil {
					t.Fatal(err)
				}
			}
			save()

			// Unforced purge.
			rr := do(t, h, http.MethodDelete, "/v1/islands/isl", "")
			if tc.wantBlock {
				if rr.Code != http.StatusConflict {
					t.Fatalf("unforced purge: got %d, want 409 (body=%s)", rr.Code, rr.Body.String())
				}
				if tc.wantSubstr != "" && !strings.Contains(rr.Body.String(), tc.wantSubstr) {
					t.Errorf("409 body = %q, want substring %q", rr.Body.String(), tc.wantSubstr)
				}
				if !strings.Contains(rr.Body.String(), "--force") {
					t.Errorf("409 body = %q, want it to mention --force", rr.Body.String())
				}
				// Forced purge succeeds.
				if rr := do(t, h, http.MethodDelete, "/v1/islands/isl?force=true", ""); rr.Code != http.StatusNoContent {
					t.Fatalf("forced purge: got %d, want 204 (body=%s)", rr.Code, rr.Body.String())
				}
			} else {
				if rr.Code != http.StatusNoContent {
					t.Fatalf("unforced purge of safe island: got %d, want 204 (body=%s)", rr.Code, rr.Body.String())
				}
			}
		})
	}
}
