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
	cases := map[string]struct {
		name string
		ok   bool
	}{
		"/v1/islands/foo":              {"foo", true},
		"/v1/islands/foo/port/intake":  {"foo", true},
		"/v1/islands/foo/files/a/b.md": {"foo", true},
		"/v1/healthz":                  {"", false},
		"/v1/islands":                  {"", false},
		"/":                            {"", false},
		// Encoded-slash bypass: ServeMux binds {name}="a/../b" (→ island "b" in the
		// handler), but a decoded-path split would see "a". Parsing the escaped
		// segment + ValidateName rejects it outright. (Security regression guard.)
		"/v1/islands/a%2F..%2Fb/port/intake": {"", false},
		"/v1/islands/..%2Fother":             {"", false},
	}
	for path, want := range cases {
		got, ok := islandFromPath(path)
		if got != want.name || ok != want.ok {
			t.Errorf("islandFromPath(%q) = (%q, %v), want (%q, %v)", path, got, ok, want.name, want.ok)
		}
	}
}

// TestTokenCrossIslandBypass is the end-to-end guard: a token for island A must
// not reach island B via an encoded-slash {name}, at the authorize layer.
func TestTokenCrossIslandBypass(t *testing.T) {
	// token island "a"; request escaped path targets "a/../b" → must be denied.
	if err := authorizeToken("a", "POST /v1/islands/{name}/port/intake", "/v1/islands/a%2F..%2Fb/port/intake"); err == nil {
		t.Fatal("encoded-slash cross-island request must be denied")
	}
	// the honest same-island request still passes
	if err := authorizeToken("a", "POST /v1/islands/{name}/port/intake", "/v1/islands/a/port/intake"); err != nil {
		t.Fatalf("same-island request should pass: %v", err)
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

		// Telemetry: any valid token may emit agent-event; the handler pins the
		// event to the token's island, so this can't spoof another island.
		{"agent-event any", "proj", "POST /v1/internal/agent-event", "/v1/internal/agent-event", false},

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
		// Resource caps / OOM priority are operator-only — a contained agent must
		// not lift its own memory limit or re-rank itself.
		{"resources denied", "proj", "PUT /v1/islands/{name}/resources", "/v1/islands/proj/resources", true},

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

		// Daemon self-update is operator-only — a contained agent must never be
		// able to make the daemon replace its own binary (absent from the
		// allow-list → default-deny).
		{"daemon update denied", "proj", "POST /v1/admin/update", "/v1/admin/update", true},
		{"daemon update denied (home)", "home", "POST /v1/admin/update", "/v1/admin/update", true},

		// SSH key enrollment is operator-only — a contained agent must never be
		// able to authorize an SSH key into the fleet (would grant itself or an
		// attacker shell access to islands).
		{"ssh key add denied", "proj", "POST /v1/ssh/account-keys", "/v1/ssh/account-keys", true},
		{"ssh key list denied", "proj", "GET /v1/ssh/account-keys", "/v1/ssh/account-keys", true},

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
	mux.HandleFunc("POST /v1/internal/agent-event", sentinel)

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
	t.Run("agent-event needs a token", func(t *testing.T) {
		if w := do("POST", "/v1/internal/agent-event", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", w.Code)
		}
	})
	t.Run("agent-event reaches handler with the token's island in ctx", func(t *testing.T) {
		// This is the anti-spoof property: whatever island the body claims,
		// handleAgentEvent attributes the event to gotIsland (the token's).
		w := do("POST", "/v1/internal/agent-event", projTok)
		if w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418 (handler reached)", w.Code)
		}
		if gotIsland != "proj" {
			t.Fatalf("ctx island=%q want proj", gotIsland)
		}
	})
}
