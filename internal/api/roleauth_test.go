package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/authtoken"
)

func idFor(role authtoken.Role, islands ...string) authtoken.Identity {
	return authtoken.Identity{Subject: "test", Role: role, Islands: islands}
}

func TestAuthorizeRoleMatrix(t *testing.T) {
	owner := idFor(authtoken.RoleOwner)
	operator := idFor(authtoken.RoleOperator)
	viewer := idFor(authtoken.RoleViewer)
	scoped := idFor(authtoken.RoleOperator, "alpha") // operator, only island alpha

	cases := []struct {
		name    string
		id      authtoken.Identity
		pattern string
		path    string
		wantErr bool
	}{
		// Read surface: every role may reach it.
		{"viewer reads status", viewer, "GET /v1/islands/{name}", "/v1/islands/alpha", false},
		{"viewer reads overview", viewer, "GET /v1/overview", "/v1/overview", false},
		{"viewer reads audit", viewer, "GET /v1/audit", "/v1/audit", false},
		{"operator reads status", operator, "GET /v1/islands/{name}", "/v1/islands/alpha", false},
		{"owner reads status", owner, "GET /v1/islands/{name}", "/v1/islands/alpha", false},

		// Operate surface: operator and owner, never viewer.
		{"viewer cannot wake", viewer, "POST /v1/islands/{name}/wake", "/v1/islands/alpha/wake", true},
		{"viewer cannot exec", viewer, "POST /v1/islands/{name}/exec", "/v1/islands/alpha/exec", true},
		{"viewer cannot attach", viewer, "GET /v1/islands/{name}/session", "/v1/islands/alpha/session", true},
		{"viewer cannot open island shell", viewer, "GET /v1/islands/{name}/shell/session", "/v1/islands/alpha/shell/session", true},
		{"viewer cannot create", viewer, "POST /v1/islands", "/v1/islands", true},
		{"operator wakes", operator, "POST /v1/islands/{name}/wake", "/v1/islands/alpha/wake", false},
		{"operator opens island shell", operator, "GET /v1/islands/{name}/shell/session", "/v1/islands/alpha/shell/session", false},
		{"viewer cannot reorder agents", viewer, "POST /v1/islands/{name}/agents/{id}/move", "/v1/islands/alpha/agents/p1/move", true},
		{"operator reorders agents", operator, "POST /v1/islands/{name}/agents/{id}/move", "/v1/islands/alpha/agents/p1/move", false},
		{"operator creates", operator, "POST /v1/islands", "/v1/islands", false},
		{"operator grants port scope", operator, "POST /v1/islands/{name}/port/scopes", "/v1/islands/alpha/port/scopes", false},

		// Purge is the operator/owner divide: operator denied, owner allowed.
		{"operator cannot purge", operator, "DELETE /v1/islands/{name}", "/v1/islands/alpha", true},
		{"owner purges", owner, "DELETE /v1/islands/{name}", "/v1/islands/alpha", false},

		// Owner-only daemon administration + credentials + tokens.
		{"operator cannot self-update", operator, "POST /v1/admin/update", "/v1/admin/update", true},
		{"operator cannot push creds", operator, "PUT /v1/credentials/claude", "/v1/credentials/claude", true},
		{"operator cannot mint tokens", operator, "POST /v1/tokens", "/v1/tokens", true},
		{"operator cannot panic", operator, "POST /v1/panic", "/v1/panic", true},
		{"operator cannot host-terminal", operator, "POST /v1/terminals", "/v1/terminals", true},
		{"owner mints tokens", owner, "POST /v1/tokens", "/v1/tokens", false},

		// Unlisted route → capOwner default (fail-closed).
		{"unlisted denies operator", operator, "POST /v1/some/future/route", "/v1/some/future/route", true},
		{"unlisted allows owner", owner, "POST /v1/some/future/route", "/v1/some/future/route", false},

		// Island scope: a scoped operator may act on its island, not others.
		{"scoped acts in-scope", scoped, "POST /v1/islands/{name}/wake", "/v1/islands/alpha/wake", false},
		{"scoped denied out-of-scope", scoped, "POST /v1/islands/{name}/wake", "/v1/islands/beta/wake", true},
		{"scoped reads in-scope", scoped, "GET /v1/islands/{name}", "/v1/islands/alpha", false},
		{"scoped denied reading out-of-scope", scoped, "GET /v1/islands/{name}", "/v1/islands/beta", true},
		// A scoped token may read fleet-wide list endpoints (no {name})...
		{"scoped reads list", scoped, "GET /v1/islands", "/v1/islands", false},
		{"scoped reads overview", scoped, "GET /v1/overview", "/v1/overview", false},
		// ...but may NOT perform a global mutation (create has no {name} to pin).
		{"scoped cannot create", scoped, "POST /v1/islands", "/v1/islands", true},

		// Encoded-slash cross-island bypass at the scope layer: alpha-scoped token
		// hitting "alpha%2F..%2Fbeta" must be denied (islandFromPath rejects it) —
		// on BOTH a mutating route and a read route, so the auth layer is
		// self-sufficient and doesn't lean on the handler's own re-validation.
		{"scoped encoded-slash denied (mutate)", scoped, "POST /v1/islands/{name}/wake", "/v1/islands/alpha%2F..%2Fbeta/wake", true},
		{"scoped encoded-slash denied (read)", scoped, "GET /v1/islands/{name}/logs", "/v1/islands/alpha%2F..%2Fbeta/logs", true},
	}
	for _, c := range cases {
		err := authorizeRole(c.id, c.pattern, c.path)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: authorizeRole(role=%s scope=%v, %q, %q) err=%v wantErr=%v",
				c.name, c.id.Role, c.id.Islands, c.pattern, c.path, err, c.wantErr)
		}
	}
}

// TestRoleRouteCapKeysAreRealRoutes asserts every pattern in roleRouteCap maps to
// a route the real mux actually registers — a typo'd key would silently never
// match, leaving that route at the capOwner default. This keeps the table in
// lockstep with routes() (the inverse — every route is classified — is covered by
// the capOwner fail-closed default for anything unlisted).
func TestRoleRouteCapKeysAreRealRoutes(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := s.routes()
	for pattern := range roleRouteCap {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Errorf("pattern %q is not 'METHOD /path'", pattern)
			continue
		}
		// Substitute concrete values for each wildcard segment.
		concrete := substituteWildcards(path)
		r := httptest.NewRequest(method, concrete, nil)
		_, matched := mux.Handler(r)
		if matched != pattern {
			t.Errorf("roleRouteCap key %q does not match a registered route (mux matched %q for %s %s)",
				pattern, matched, method, concrete)
		}
	}
}

// substituteWildcards turns a route pattern path into a concrete request path by
// replacing each {wildcard} segment with "x" (and {path...} with a single segment).
func substituteWildcards(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}

// TestRoleAuthMiddleware drives the full operator middleware: anonymous → owner,
// present-but-unknown → 401, a real token attenuates, and --require-token rejects
// anonymous callers. It stamps the resolved Identity onto the context.
func TestRoleAuthMiddleware(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	viewerSecret, _, err := authtoken.Create("v", authtoken.RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	opScopedSecret, _, err := authtoken.Create("o", authtoken.RoleOperator, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}

	var gotIdentity authtoken.Identity
	var gotOK bool
	var gotAudit AuditIdentity
	var gotAuditOK bool
	sentinel := func(w http.ResponseWriter, r *http.Request) {
		gotIdentity, gotOK = IdentityFromContext(r.Context())
		gotAudit, gotAuditOK = AuditIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/islands", sentinel)
	mux.HandleFunc("POST /v1/islands/{name}/wake", sentinel)
	mux.HandleFunc("DELETE /v1/islands/{name}", sentinel)

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.roleAuth(mux, mux)

	do := func(method, path, token string) *httptest.ResponseRecorder {
		gotIdentity, gotOK = authtoken.Identity{}, false
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	t.Run("anonymous is trusted owner", func(t *testing.T) {
		w := do("DELETE", "/v1/islands/alpha", "")
		if w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418 (owner reaches purge)", w.Code)
		}
		if !gotOK || gotIdentity.Role != authtoken.RoleOwner || gotIdentity.Subject != "local" {
			t.Fatalf("identity=%+v ok=%v want local owner", gotIdentity, gotOK)
		}
		// The #1 seam: roleAuth also stamps Lane 1's AuditIdentity so the audit
		// middleware attributes the record to who/role.
		if !gotAuditOK || gotAudit.Actor != "local" || gotAudit.Role != "owner" {
			t.Fatalf("audit identity=%+v ok=%v want {local owner}", gotAudit, gotAuditOK)
		}
	})
	t.Run("unknown token 401", func(t *testing.T) {
		if w := do("GET", "/v1/islands", "deadbeef"); w.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", w.Code)
		}
	})
	t.Run("viewer reads list", func(t *testing.T) {
		w := do("GET", "/v1/islands", viewerSecret)
		if w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418", w.Code)
		}
		if gotIdentity.Role != authtoken.RoleViewer {
			t.Fatalf("role=%s want viewer", gotIdentity.Role)
		}
		// AuditIdentity carries the token's label + role for audit attribution.
		if gotAudit.Actor != "v" || gotAudit.Role != "viewer" {
			t.Fatalf("audit identity=%+v want {v viewer}", gotAudit)
		}
	})
	t.Run("viewer cannot wake 403", func(t *testing.T) {
		if w := do("POST", "/v1/islands/alpha/wake", viewerSecret); w.Code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", w.Code)
		}
	})
	t.Run("scoped operator wakes its island", func(t *testing.T) {
		if w := do("POST", "/v1/islands/alpha/wake", opScopedSecret); w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418", w.Code)
		}
	})
	t.Run("scoped operator denied other island 403", func(t *testing.T) {
		if w := do("POST", "/v1/islands/beta/wake", opScopedSecret); w.Code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", w.Code)
		}
	})
	t.Run("operator cannot purge 403", func(t *testing.T) {
		if w := do("DELETE", "/v1/islands/alpha", opScopedSecret); w.Code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", w.Code)
		}
	})

	t.Run("require-token rejects anonymous", func(t *testing.T) {
		s.RequireToken()
		defer func() { s.requireToken = false }()
		if w := do("GET", "/v1/islands", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401 under require-token", w.Code)
		}
		// A valid token still works under require-token.
		if w := do("GET", "/v1/islands", viewerSecret); w.Code != http.StatusTeapot {
			t.Fatalf("code=%d want 418 (token still accepted)", w.Code)
		}
	})
}
