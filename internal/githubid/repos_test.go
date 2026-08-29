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

	res, err := listRepos(context.Background(), srv.URL, "tok123", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("want 2 repos, got %d", len(res.Repos))
	}
	if res.Capped {
		t.Error("Capped should be false when there's no next-page Link header")
	}
	if res.Repos[0].NameWithOwner != "me/app" || res.Repos[0].URL != "https://github.com/me/app.git" || !res.Repos[0].Private {
		t.Errorf("repo[0] mismapped: %+v", res.Repos[0])
	}
	if res.Repos[1].Private {
		t.Errorf("repo[1] should be public: %+v", res.Repos[1])
	}
}

func TestListReposReportsCappedFromLinkHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://api.github.com/user/repos?page=2>; rel="next", `+
			`<https://api.github.com/user/repos?page=5>; rel="last"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"full_name":"me/app","clone_url":"https://x/app.git"}]`))
	}))
	defer srv.Close()
	res, err := listRepos(context.Background(), srv.URL, "tok", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped {
		t.Error("Capped should be true when a rel=\"next\" Link is present")
	}
}

func TestVerifyToken(t *testing.T) {
	t.Run("returns login on 200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user" {
				t.Errorf("path = %q, want /user", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer good" {
				t.Errorf("auth header = %q", r.Header.Get("Authorization"))
			}
			// GitHub returns the token's scopes on this very call. They were always
			// here and always discarded, which is why "your token authenticates"
			// was the strongest thing any surface could say about it.
			w.Header().Set("X-OAuth-Scopes", "repo, read:org")
			_, _ = w.Write([]byte(`{"login":"octocat","id":583231}`))
		}))
		defer srv.Close()
		login, id, scopes, err := verifyToken(context.Background(), srv.URL, "good")
		if err != nil {
			t.Fatal(err)
		}
		if login != "octocat" {
			t.Errorf("login = %q, want octocat", login)
		}
		if id != 583231 {
			t.Errorf("id = %d, want 583231", id)
		}
		if scopes != "repo, read:org" {
			t.Errorf("scopes = %q, want the X-OAuth-Scopes header verbatim — without it "+
				"a token that authenticates and cannot open a pull request is "+
				"indistinguishable from a working one", scopes)
		}
	})

	t.Run("errors on 401", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		}))
		defer srv.Close()
		if _, _, _, err := verifyToken(context.Background(), srv.URL, "bad"); err == nil {
			t.Fatal("expected an error on 401")
		}
	})
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
