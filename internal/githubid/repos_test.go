package githubid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApiBase(t *testing.T) {
	if got := apiBase(""); got != "https://api.github.com" {
		t.Errorf("empty host = %q", got)
	}
	if got := apiBase("github.com"); got != "https://api.github.com" {
		t.Errorf("public host = %q", got)
	}
	if got := apiBase("github.example.com"); got != "https://github.example.com/api/v3" {
		t.Errorf("enterprise host = %q", got)
	}
}

func TestListReposParsesAndAuthenticates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			t.Errorf("missing/wrong auth header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/user/repos" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"full_name":"me/app","clone_url":"https://github.com/me/app.git","description":"d","private":true},
			{"full_name":"org/lib","clone_url":"https://github.com/org/lib.git","description":"","private":false}
		]`))
	}))
	defer srv.Close()

	repos, err := listRepos(context.Background(), srv.URL, "tok123", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("want 2 repos, got %d", len(repos))
	}
	if repos[0].NameWithOwner != "me/app" || repos[0].URL != "https://github.com/me/app.git" || !repos[0].Private {
		t.Errorf("repo[0] mismapped: %+v", repos[0])
	}
	if repos[1].Private {
		t.Errorf("repo[1] should be public: %+v", repos[1])
	}
}

func TestListReposSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	if _, err := listRepos(context.Background(), srv.URL, "bad", 10); err == nil {
		t.Fatal("expected an error on 401")
	}
}
