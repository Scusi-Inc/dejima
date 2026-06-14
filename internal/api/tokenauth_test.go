package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoos/dejima/internal/porttoken"
	"github.com/aoos/dejima/internal/project"
)

// seedIsland creates an on-disk project (HOME must already be isolated by the
// caller via t.Setenv) and mints its token, returning the token.
func seedIsland(t *testing.T, name, role string) string {
	t.Helper()
	p := &project.Project{Name: name, Role: role}
	if err := p.Save(); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	tok, err := porttoken.Ensure(name)
	if err != nil {
		t.Fatalf("ensure token %s: %v", name, err)
	}
	return tok
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc123", "abc123"},
		{"bearer abc123", "abc123"},     // scheme is case-insensitive
		{"BEARER abc123", "abc123"},     //
		{"Bearer   abc123  ", "abc123"}, // surrounding space trimmed
		{"", ""},
		{"abc123", ""},       // no scheme
		{"Basic abc123", ""}, // wrong scheme
		{"Bearer", ""},       // scheme only
		{"Bearer ", ""},      // empty token
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := bearerToken(r); got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestIslandFromPath(t *testing.T) {
	cases := []struct {
		path   string
		want   string
		wantOK bool
	}{
		{"/v1/islands/foo", "foo", true},
		{"/v1/islands/foo/port/intake", "foo", true},
		{"/v1/islands/foo/files/a/b.md", "foo", true},
		{"/v1/healthz", "", false},
		{"/v1/islands", "", false},
		{"/", "", false},
		// Encoded-slash traversal: the segment unescapes to a non-name and must
		// be rejected, not parsed as "a" (the cross-island bypass).
		{"/v1/islands/a%2F..%2Fb/exec", "", false},
		{"/v1/islands/a%2Fb/exec", "", false},
		{"/v1/islands/%2e%2e/exec", "", false}, // ".." → invalid name
		{"/v1/islands/A/exec", "", false},      // uppercase fails nameRe
	}
	for _, c := range cases {
		got, ok := islandFromPath(c.path)
		if got != c.want || ok != c.wantOK {
			t.Errorf("islandFromPath(%q) = (%q,%v), want (%q,%v)", c.path, got, ok, c.want, c.wantOK)
		}
	}
}

func TestAuthorizeTokenMatrix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedIsland(t, "home", project.RoleHome)
	seedIsland(t, "proj", project.RoleProject)

	cases := []struct {
		name    string
		island  string
		pattern string
		path    string
		wantErr bool
	}{
		// Reachability probe: any island token.
		{"healthz any", "proj", "GET /v1/healthz", "/v1/healthz", false},

		// Own-island autonomy surface.
		{"intake own", "proj", "POST /v1/islands/{name}/port/intake", "/v1/islands/proj/port/intake", false},
		{"write own", "proj", "POST /v1/islands/{name}/port/write", "/v1/islands/proj/port/write", false},
		{"export own", "proj", "POST /v1/islands/{name}/port/export", "/v1/islands/proj/port/export", false},
		{"exec own", "proj", "POST /v1/islands/{name}/exec", "/v1/islands/proj/exec", false},
		{"status own", "proj", "GET /v1/islands/{name}", "/v1/islands/proj", false},
		{"events own", "proj", "GET /v1/islands/{name}/events", "/v1/islands/proj/events", false},
		{"logs own", "proj", "GET /v1/islands/{name}/logs", "/v1/islands/proj/logs", false},
		{"list scopes own", "proj", "GET /v1/islands/{name}/port/scopes", "/v1/islands/proj/port/scopes", false},
		{"read file own", "proj", "GET /v1/islands/{name}/files/{path...}", "/v1/islands/proj/files/x", false},
		{"write file own", "proj", "PUT /v1/islands/{name}/files/{path...}", "/v1/islands/proj/files/x", false},

		// Cross-island: the crux. Same route, another island → denied.
		{"intake other", "proj", "POST /v1/islands/{name}/port/intake", "/v1/islands/home/port/intake", true},
		{"exec other", "proj", "POST /v1/islands/{name}/exec", "/v1/islands/home/exec", true},
		// Encoded-slash traversal: pattern matches as own-island, but the name
		// segment unescapes to "proj/../home" — must be denied, not read as "proj".
		{"exec encoded traversal", "proj", "POST /v1/islands/{name}/exec", "/v1/islands/proj%2F..%2Fhome/exec", true},
		{"intake encoded traversal", "proj", "POST /v1/islands/{name}/port/intake", "/v1/islands/proj%2F..%2Fhome/port/intake", true},

		// island.create gated on Home role.
		{"create as home", "home", "POST /v1/islands", "/v1/islands", false},
		{"create as proj", "proj", "POST /v1/islands", "/v1/islands", true},

		// Containment-control: granting/revoking scopes is operator-only.
		{"grant scope denied", "proj", "POST /v1/islands/{name}/port/scopes", "/v1/islands/proj/port/scopes", true},
		{"revoke scope denied", "proj", "DELETE /v1/islands/{name}/port/scopes/{scope}", "/v1/islands/proj/port/scopes/s", true},

		// Lifecycle and attach surface denied even on own island.
		{"delete denied", "proj", "DELETE /v1/islands/{name}", "/v1/islands/proj", true},
		{"reset denied", "proj", "POST /v1/islands/{name}/reset", "/v1/islands/proj/reset", true},
		{"session denied", "proj", "GET /v1/islands/{name}/session", "/v1/islands/proj/session", true},

		// Host terminals are the most privileged surface — operator-only, never
		// reachable by an island token (absent from the allow-list → default-deny).
		{"terminals list denied", "proj", "GET /v1/terminals", "/v1/terminals", true},
		{"terminals create denied", "proj", "POST /v1/terminals", "/v1/terminals", true},
		{"terminal delete denied", "proj", "DELETE /v1/terminals/{id}", "/v1/terminals/t1", true},
		{"terminal attach denied", "proj", "GET /v1/terminals/{id}/session", "/v1/terminals/t1/session", true},

		// Control plane denied.
		{"list all denied", "proj", "GET /v1/islands", "/v1/islands", true},
		{"overview denied", "proj", "GET /v1/overview", "/v1/overview", true},
		{"creds denied", "proj", "PUT /v1/credentials/claude", "/v1/credentials/claude", true},

		// Unmatched pattern (non-canonical path / unknown route) → denied.
		{"empty pattern denied", "proj", "", "/v1/islands/proj/../home/exec", true},
	}
	for _, c := range cases {
		err := authorizeToken(c.island, c.pattern, c.path)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: authorizeToken(%q,%q,%q) err=%v, wantErr=%v",
				c.name, c.island, c.pattern, c.path, err, c.wantErr)
		}
	}
}

// TestTokenAuthMiddleware drives the full middleware against a sentinel mux so
// authentication, classification, scoping, and context-stamping are exercised
// together with real on-disk tokens.
func TestTokenAuthMiddleware(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	homeTok := seedIsland(t, "home", project.RoleHome)
	projTok := seedIsland(t, "proj", project.RoleProject)

	// Sentinel mux mirroring the real patterns we assert on. The handler
	// records the island the middleware stamped into the context.
	var gotIsland string
	sentinel := func(w http.ResponseWriter, r *http.Request) {
		gotIsland = TokenIslandFromContext(r.Context())
		w.WriteHeader(http.StatusTeapot) // distinct from 200 so we know we arrived
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", sentinel)
	mux.HandleFunc("POST /v1/islands", sentinel)
	mux.HandleFunc("POST /v1/islands/{name}/port/intake", sentinel)

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.tokenAuth(mux)

	do := func(method, path, token string) *httptest.ResponseRecorder {
		gotIsland = ""
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	t.Run("missing token 401", func(t *testing.T) {
		if w := do("GET", "/v1/healthz", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", w.Code)
		}
	})
	t.Run("garbage token 401", func(t *testing.T) {
		if w := do("GET", "/v1/healthz", "not-a-real-token"); w.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", w.Code)
		}
	})
	t.Run("own intake reaches handler with island in ctx", func(t *testing.T) {
		w := do("POST", "/v1/islands/proj/port/intake", projTok)
		if w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418 (handler reached)", w.Code)
		}
		if gotIsland != "proj" {
			t.Fatalf("ctx island=%q want proj", gotIsland)
		}
	})
	t.Run("cross-island intake 403", func(t *testing.T) {
		w := do("POST", "/v1/islands/home/port/intake", projTok)
		if w.Code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", w.Code)
		}
	})
	t.Run("encoded-slash traversal to other island 403", func(t *testing.T) {
		// proj's token, route binds {name}="proj/../home" which would Clean to
		// home; the escaped-path parse must reject it before the handler runs.
		w := do("POST", "/v1/islands/proj%2F..%2Fhome/port/intake", projTok)
		if w.Code != http.StatusForbidden {
			t.Fatalf("code=%d want 403 (got %d — cross-island bypass!)", w.Code, w.Code)
		}
	})
	t.Run("project token cannot create 403", func(t *testing.T) {
		if w := do("POST", "/v1/islands", projTok); w.Code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", w.Code)
		}
	})
	t.Run("home token can create", func(t *testing.T) {
		if w := do("POST", "/v1/islands", homeTok); w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418 (handler reached)", w.Code)
		}
	})
}
