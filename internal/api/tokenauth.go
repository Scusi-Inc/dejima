package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/aoos/dejima/internal/porttoken"
	"github.com/aoos/dejima/internal/project"
)

// This file is the security boundary for the macOS autonomy path (runbook §5,
// port-island-spec §11.3): the only way an agent *inside* an island reaches
// dejimad. The unix socket (Linux) and the tailnet TCP listener are
// fully-trusted and never reach this code; TokenAuthHandler is wired solely to
// the host-internal loopback listener. Every decision here is default-deny.
//
// Threat model: a compromised in-island brain holds its own island's bearer
// token and tries to (a) reach the host, (b) touch another island, or
// (c) escalate its own island's containment. The middleware below stops all
// three: it authenticates the token (constant-time, in porttoken), pins every
// request to the token's island, and exposes only the autonomy surface —
// never the operator/control-plane or containment-granting routes.

// tokenAccess classifies how an island bearer token may reach a route.
type tokenAccess int

const (
	// accessDeny is the default for every route not explicitly listed below:
	// the whole control plane (list/overview/clients/credentials/image build/
	// webhooks/sessions), all lifecycle ops (delete/reset/hibernate/wake/
	// upgrade/patch), the attach surface (session/agents), and — critically —
	// Port scope grant/revoke, which is the operator's act of authorizing host
	// access and must never be self-service for a contained brain.
	accessDeny tokenAccess = iota
	// accessAny is reachable by any valid token regardless of its island. Used
	// only for the data-free reachability probe.
	accessAny
	// accessOwnIsland is reachable only when the route's {name} equals the
	// token's island — the autonomy surface (port trade, exec, files, logs,
	// status, events, list-own-scopes).
	accessOwnIsland
	// accessHomeCreate is island.create: allowed only for a Home Island token,
	// which spawns Project Islands and receives the child's token (no
	// god-token; see porttoken).
	accessHomeCreate
	// accessEmitOwn is reachable by any valid token; the handler attributes the
	// event to the token's island (TokenIslandFromContext), so a token can emit
	// telemetry only for its own island — never spoof another's. Used for the
	// agent-event endpoint, whose path carries no {name} to pin against.
	accessEmitOwn
)

// tokenRouteAccess maps the exact ServeMux pattern (as registered in routes())
// to its access class. A route absent from this map is denied. Keying on the
// matched *pattern* — not a hand-parsed path — keeps this table in lockstep
// with the router: a non-canonical path matches no pattern and is denied.
var tokenRouteAccess = map[string]tokenAccess{
	"GET /v1/healthz":               accessAny,
	"POST /v1/islands":              accessHomeCreate,
	"POST /v1/internal/agent-event": accessEmitOwn,

	"GET /v1/islands/{name}":                 accessOwnIsland, // status
	"GET /v1/islands/{name}/events":          accessOwnIsland,
	"GET /v1/islands/{name}/logs":            accessOwnIsland,
	"GET /v1/islands/{name}/port/scopes":     accessOwnIsland, // list own grants only
	"POST /v1/islands/{name}/port/intake":    accessOwnIsland,
	"POST /v1/islands/{name}/port/export":    accessOwnIsland,
	"POST /v1/islands/{name}/port/write":     accessOwnIsland,
	"POST /v1/islands/{name}/exec":           accessOwnIsland,
	"GET /v1/islands/{name}/files/{path...}": accessOwnIsland,
	"PUT /v1/islands/{name}/files/{path...}": accessOwnIsland,
}

// tokenIslandKey carries the authenticated island down to handlers (for audit
// attribution and the spawn-returns-child-token flow).
type tokenIslandKey struct{}

// TokenIslandFromContext returns the island a token-authenticated request was
// scoped to, or "" for requests that did not arrive on the token listener.
func TokenIslandFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tokenIslandKey{}).(string)
	return v
}

// TokenAuthHandler returns the API handler for the host-internal, token-
// authenticated TCP listener — the in-island → dejimad autonomy path. It must
// only ever back a listener bound to a host-internal address (loopback,
// reachable via host.docker.internal), never a LAN/0.0.0.0 bind: the token is
// the authorization, the bind limits exposure.
func (s *Server) TokenAuthHandler() http.Handler {
	mux := s.routes()
	return logMiddleware(s.log, s.tokenAuth(mux))
}

// tokenAuth authenticates the bearer token, pins the request to the token's
// island, and rejects anything outside that island's autonomy surface.
func (s *Server) tokenAuth(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, errors.New("missing bearer token"))
			return
		}
		island, ok := porttoken.IslandForToken(tok)
		if !ok {
			// porttoken compares in constant time; a bad token and an unknown
			// token are indistinguishable to the caller.
			writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
			return
		}

		// Classify on the pattern the router will actually dispatch to, so the
		// authorization decision can never diverge from routing. Authorize on
		// the *escaped* path so an encoded slash in {name} can't be parsed as a
		// different island than the router binds (see islandFromPath).
		_, pattern := mux.Handler(r)
		if err := authorizeToken(island, pattern, r.URL.EscapedPath()); err != nil {
			s.log.Warn("token request denied",
				"island", island, "method", r.Method, "path", r.URL.Path, "reason", err)
			writeError(w, http.StatusForbidden, err)
			return
		}

		ctx := context.WithValue(r.Context(), tokenIslandKey{}, island)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authorizeToken decides whether the token for `island` may take the request
// matched to `pattern` (escapedPath is r.URL.EscapedPath()). Default-deny.
func authorizeToken(island, pattern, escapedPath string) error {
	switch tokenRouteAccess[pattern] {
	case accessAny:
		return nil

	case accessEmitOwn:
		// Any valid token may emit; handleAgentEvent pins the event to the
		// token's island, so cross-island spoofing is impossible.
		return nil

	case accessOwnIsland:
		target, ok := islandFromPath(escapedPath)
		if !ok {
			return errors.New("invalid or missing target island")
		}
		if target != island {
			// The crux: an island token may never act on another island.
			return errors.New("token is scoped to a different island")
		}
		return nil

	case accessHomeCreate:
		p, err := project.Load(island)
		if err != nil {
			return errors.New("could not verify island role")
		}
		if !p.IsHome() {
			return errors.New("only a Home Island may create islands")
		}
		return nil

	default: // accessDeny — unlisted route, control plane, lifecycle, or grant.
		return errors.New("route not permitted for an island token")
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, or "" if absent/malformed. The scheme match is case-insensitive per
// RFC 7235; the token itself is compared (downstream) in constant time.
func bearerToken(r *http.Request) string {
	const scheme = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(h[len(scheme):])
}

// islandFromPath pulls the {name} segment out of an island-scoped request path
// and validates it. It parses the *escaped* path on purpose: ServeMux binds the
// {name} wildcard to a single segment that may contain a percent-encoded slash
// (e.g. "a%2F..%2Fb"), which it then hands to handlers decoded as "a/../b" —
// and project.Load runs that through filepath.Join/Clean, resolving it to a
// DIFFERENT island ("b"). Splitting the *decoded* URL.Path would make this auth
// parse see only "a" and pass, while the handler acts on "b". Parsing the
// escaped segment and requiring it to be one valid island name (no separator,
// no traversal — project.ValidateName enforces exactly the charset Load
// accepts) keeps the authorization identity in lockstep with what the router
// binds. ok=false ⇒ deny.
func islandFromPath(escapedPath string) (name string, ok bool) {
	parts := strings.Split(escapedPath, "/")
	// ["", "v1", "islands", "{name}", ...]
	if len(parts) < 4 || parts[1] != "v1" || parts[2] != "islands" {
		return "", false
	}
	seg, err := url.PathUnescape(parts[3])
	if err != nil {
		return "", false
	}
	if project.ValidateName(seg) != nil {
		return "", false
	}
	return seg, true
}
