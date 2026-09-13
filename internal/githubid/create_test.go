package githubid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A classic token without `repo` must be refused BEFORE the network call.
//
// This is the lesson ScopeNote was written for, applied one layer up. Such a
// token authenticates perfectly, lists repos perfectly, and cannot create one —
// GitHub answers with a 403 about "resources", never about scopes. Letting it
// through means the operator confirms a create, waits, and gets a message that
// names nothing they can act on.
func TestCreateRepoRefusesATokenThatCannotWrite(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	id := Identity{Name: "work", Login: "octocat", Token: "tok", Scopes: "read:org,gist"}
	_, err := CreateRepo(context.Background(), id, NewRepo{Name: "app", Private: true})
	if err == nil {
		t.Fatal("a token with no repo scope was allowed to attempt a create")
	}
	if !strings.Contains(err.Error(), "repo") {
		t.Errorf("the error must name the missing scope, got: %v", err)
	}
	if reached {
		t.Error("the create call went out despite the scope check")
	}
}

// A FINE-GRAINED token reports no scopes and must NOT be refused.
//
// The control for the test above. "" means "not introspectable", not "no
// permissions" — collapsing those two is the exact bug ScopeNote's comment
// describes, and a guard that rejects both would make every fine-grained token
// useless here while looking like careful security.
func TestCreateRepoAllowsAFineGrainedToken(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": "octocat/app", "clone_url": "https://x/app.git", "private": true,
		})
	}))
	defer srv.Close()

	repo, err := createRepo(context.Background(), srv.URL, "tok", NewRepo{Name: "app", Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("the request never went out")
	}
	if repo.NameWithOwner != "octocat/app" || !repo.Private {
		t.Errorf("repo = %+v", repo)
	}
	// And the scope gate itself lets "" through, which is the half that matters.
	if _, canWrite := ScopeNote(""); !canWrite {
		t.Error("a fine-grained token is being treated as unable to write")
	}
}

// The request body must carry auto_init, or the island clones a repo with no
// commit and no default branch.
func TestCreateRepoAsksForAnInitialCommit(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "octocat/app"})
	}))
	defer srv.Close()

	if _, err := createRepo(context.Background(), srv.URL, "tok", NewRepo{Name: "app", Private: true}); err != nil {
		t.Fatal(err)
	}
	if body["auto_init"] != true {
		t.Fatalf("auto_init was not requested (%v) — the island would clone an empty "+
			"repo with no HEAD, and the first `git switch -c x origin/main` would fail", body["auto_init"])
	}
	if body["private"] != true {
		t.Errorf("private did not reach the request body: %v", body["private"])
	}
}

// GitHub's nested 422 reason must survive into the error.
func TestCreateRepoSurfacesTheNestedReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Repository creation failed.","errors":[{"field":"name","message":"name already exists on this account"}]}`))
	}))
	defer srv.Close()

	_, err := createRepo(context.Background(), srv.URL, "tok", NewRepo{Name: "app"})
	if err == nil {
		t.Fatal("a 422 was reported as success")
	}
	if !strings.Contains(err.Error(), "name already exists") {
		t.Errorf("the actionable reason was dropped; got: %v", err)
	}
}

func TestValidateRepoName(t *testing.T) {
	ok := []string{"app", "my-repo", "my_repo", "dot.name", "a", strings.Repeat("x", 100)}
	for _, n := range ok {
		if err := ValidateRepoName(n); err != nil {
			t.Errorf("ValidateRepoName(%q) = %v, want nil", truncName(n), err)
		}
	}
	bad := []string{"", "   ", "has space", "sl/ash", "back\\slash", "emoji🙂", ".", "..", strings.Repeat("x", 101)}
	for _, n := range bad {
		if err := ValidateRepoName(n); err == nil {
			t.Errorf("ValidateRepoName(%q) = nil, want an error", truncName(n))
		}
	}
}

func truncName(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}
