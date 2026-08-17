package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// createGuardCase drives just the request validation at the top of createIsland,
// which is where every no_repo decision is made.
func postCreate(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/islands", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.createIsland(w, r)
	return w.Code, w.Body.String()
}

// The point of a separate no_repo flag is that an EMPTY repo stays an error. A
// URL eaten by the shell, or a variable that expanded to nothing, must fail
// loudly — an empty island that boots fine is indistinguishable from a clone
// that silently didn't happen, which is the failure this codebase keeps finding.
func TestCreate_EmptyRepoStillRejected(t *testing.T) {
	s := &Server{}
	code, body := postCreate(t, s, `{"name":"scratch"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("empty repo with no no_repo should be rejected, got %d: %s", code, body)
	}
	if !strings.Contains(body, "no_repo") {
		t.Errorf("the error should point at the deliberate alternative, got: %s", body)
	}
}

// no_repo names an island with nothing to derive a name FROM, so the name stops
// being optional. Silently generating one would produce islands nobody can
// predict the name of.
func TestCreate_NoRepoRequiresName(t *testing.T) {
	s := &Server{}
	code, body := postCreate(t, s, `{"no_repo":true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("no_repo without a name should be rejected, got %d: %s", code, body)
	}
	if !strings.Contains(body, "name is required") {
		t.Errorf("error should say a name is required, got: %s", body)
	}
}

// Asking for both is a contradiction, and guessing which one the caller meant is
// how you end up cloning into an island someone believed was empty.
func TestCreate_NoRepoConflictsWithRepoAndSeed(t *testing.T) {
	for _, body := range []string{
		`{"name":"x","no_repo":true,"repo":"https://github.com/a/b"}`,
		`{"name":"x","no_repo":true,"seed_path":"/tmp/seed"}`,
	} {
		code, resp := postCreate(t, &Server{}, body)
		if code != http.StatusBadRequest {
			t.Errorf("expected rejection for %s, got %d: %s", body, code, resp)
		}
		if !strings.Contains(resp, "pick one") {
			t.Errorf("error should say they're exclusive, got: %s", resp)
		}
	}
}

// The regression that makes a working island look broken. workspaceReady probes
// for /workspace/.git; a repo-less island never has one, so the probe can only
// fail. Left alone the caller polls its full two-minute budget and then reports
// "stalled" — the slowest possible way to present a healthy island as a failure.
func TestWorkspaceReady_NoRepoIsImmediatelyReady(t *testing.T) {
	// A nil runtime is the assertion: if this path ever reaches the container
	// probe it will panic rather than quietly pass. Readiness must be decided
	// from the record alone.
	t.Setenv("HOME", t.TempDir()) // redirect ~/.dejima at the project store
	p := &project.Project{Name: "brain", NoRepo: true, DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatalf("save project: %v", err)
	}

	s := &Server{rt: nil}
	r := httptest.NewRequest(http.MethodGet, "/v1/islands/brain/workspace-ready", nil)
	r.SetPathValue("name", "brain")
	w := httptest.NewRecorder()
	s.workspaceReady(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp WorkspaceReadyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Ready {
		t.Error("a repo-less island has nothing to clone and must be ready at once; " +
			"otherwise the caller waits out its whole budget and reports a false stall")
	}
	if resp.CloneFailed {
		t.Error("nothing was cloned, so nothing can have failed to clone")
	}
}
