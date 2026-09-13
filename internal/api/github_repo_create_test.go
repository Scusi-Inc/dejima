package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/runtime"
)

// newRepoCreateServer wires a server whose GitHub create is a stub, so these
// tests can cover the handler without making repositories on a real account.
func newRepoCreateServer(t *testing.T, create func(context.Context, githubid.Identity, githubid.NewRepo) (githubid.Repo, error)) http.Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := githubid.Update(func(s *githubid.Store) error {
		s.Put(githubid.Identity{Name: "work", Login: "octocat", Token: "tok", Scopes: "repo"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	srv.repoCreate = create
	return srv.Handler()
}

// The private flag must reach GitHub exactly as sent.
//
// The whole feature rests on this one boolean. Everything else it gets wrong is
// an inconvenience; getting this wrong publishes someone's source, and it fails
// in the direction that looks fine — a public repo is created successfully, the
// island builds, and nothing anywhere reports a problem.
func TestCreateRepoPassesVisibilityThrough(t *testing.T) {
	for _, private := range []bool{true, false} {
		var got githubid.NewRepo
		h := newRepoCreateServer(t, func(_ context.Context, _ githubid.Identity, spec githubid.NewRepo) (githubid.Repo, error) {
			got = spec
			return githubid.Repo{NameWithOwner: "octocat/app", URL: "https://x/app.git", Private: spec.Private}, nil
		})
		body := `{"name":"app","private":` + map[bool]string{true: "true", false: "false"}[private] + `}`
		rr := do(t, h, http.MethodPost, "/v1/credentials/github/work/repos", body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("private=%v: %d %s", private, rr.Code, rr.Body.String())
		}
		if got.Private != private {
			t.Errorf("private=%v reached GitHub as %v", private, got.Private)
		}
	}
}

// An omitted "private" field must NOT quietly create a public repo... except it
// does, because that is what JSON says a missing bool means — so the contract is
// that clients always send it, and this test pins the behaviour so the next
// person reads it deliberately rather than discovering it.
//
// Pinned rather than "fixed" with a pointer: every in-tree caller sends the
// field, the TUI defaults it to private, and making the wire format tri-state
// buys a third case to get wrong. What it must never be is UNDOCUMENTED, which
// is what it was before this test.
func TestCreateRepoOmittedVisibilityIsPublic(t *testing.T) {
	var got githubid.NewRepo
	h := newRepoCreateServer(t, func(_ context.Context, _ githubid.Identity, spec githubid.NewRepo) (githubid.Repo, error) {
		got = spec
		return githubid.Repo{NameWithOwner: "octocat/app", Private: spec.Private}, nil
	})
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/work/repos", `{"name":"app"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if got.Private {
		t.Fatal("a missing private field created a PRIVATE repo — if this is now the " +
			"behaviour, good, but CreateGitHubRepoRequest's doc comment says the " +
			"opposite and one of them is lying to the next reader")
	}
}

// The response must be GitHub's answer, not an echo of the request.
//
// An org policy can force a repo public when private was asked for. GitHub
// reports that only in the create response, so a handler that echoes the request
// makes the override invisible at the one moment the operator could still act on
// it.
func TestCreateRepoReturnsWhatGitHubActuallyMade(t *testing.T) {
	h := newRepoCreateServer(t, func(_ context.Context, _ githubid.Identity, _ githubid.NewRepo) (githubid.Repo, error) {
		// Asked private; org policy says otherwise.
		return githubid.Repo{NameWithOwner: "octocat/app", URL: "https://x/app.git", Private: false}, nil
	})
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/work/repos", `{"name":"app","private":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var resp CreateGitHubRepoResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Repo.Private {
		t.Fatal("the response echoed the REQUEST's visibility instead of GitHub's — " +
			"an org policy forcing a repo public would be invisible to the caller")
	}
}

// A bad name must fail before anything is created.
func TestCreateRepoRejectsBadNameWithoutCallingGitHub(t *testing.T) {
	called := false
	h := newRepoCreateServer(t, func(_ context.Context, _ githubid.Identity, _ githubid.NewRepo) (githubid.Repo, error) {
		called = true
		return githubid.Repo{}, nil
	})
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/work/repos", `{"name":"has spaces","private":true}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for an invalid name, got %d %s", rr.Code, rr.Body.String())
	}
	if called {
		t.Error("an invalid name still reached GitHub")
	}
}

// An unknown identity is a 404, not a create against the default.
func TestCreateRepoUnknownIdentity(t *testing.T) {
	called := false
	h := newRepoCreateServer(t, func(_ context.Context, _ githubid.Identity, _ githubid.NewRepo) (githubid.Repo, error) {
		called = true
		return githubid.Repo{}, nil
	})
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/nope/repos", `{"name":"app","private":true}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d %s", rr.Code, rr.Body.String())
	}
	if called {
		t.Error("an unknown identity still created a repo — under whose account?")
	}
}

// The identity's token must reach the create call.
//
// A create that silently ran as the wrong identity would put a repo on the wrong
// GitHub account, which is exactly the mistake the confirm screen exists to
// prevent — and the screen is worthless if the name it shows is not the name
// that acts.
func TestCreateRepoActsAsTheNamedIdentity(t *testing.T) {
	var gotName, gotToken string
	h := newRepoCreateServer(t, func(_ context.Context, id githubid.Identity, _ githubid.NewRepo) (githubid.Repo, error) {
		gotName, gotToken = id.Name, id.Token
		return githubid.Repo{NameWithOwner: "octocat/app"}, nil
	})
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/work/repos", `{"name":"app","private":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if gotName != "work" || gotToken != "tok" {
		t.Errorf("create ran as %q/%q, want work/tok", gotName, gotToken)
	}
}

// An upstream failure is a 502 carrying GitHub's reason.
//
// "422 Unprocessable Entity" with the reason dropped is the failure this
// replaced: the overwhelmingly likely error here is a name already in use, and
// that is actionable only if it survives to the operator.
func TestCreateRepoSurfacesGitHubsReason(t *testing.T) {
	h := newRepoCreateServer(t, func(_ context.Context, _ githubid.Identity, _ githubid.NewRepo) (githubid.Repo, error) {
		return githubid.Repo{}, errNameTaken{}
	})
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/work/repos", `{"name":"app","private":true}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "name already exists") {
		t.Errorf("GitHub's reason did not survive to the client: %s", rr.Body.String())
	}
}

type errNameTaken struct{}

func (errNameTaken) Error() string {
	return "github refused to create the repository: name already exists on this account"
}
