package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aoos/dejima/internal/authtoken"
	"github.com/aoos/dejima/internal/project"
)

// This file is the team-auth boundary for the *operator* API surface — the unix
// socket and the tailnet-pinned TCP listener that back Server.Handler(). It is
// the sibling of tokenauth.go: tokenauth governs the in-island autonomy listener
// (a contained brain reaching the host), while roleauth governs operator clients
// (a human or a wrapper service driving the control plane).
//
// The model is *exchange-down* (docs/security-boundary.md): a request with no
// bearer token is the fully-trusted caller the listener already vouches for
// (filesystem perms on the socket; tailnet pinning on TCP) and runs as `owner`.
// A request that *does* carry a token is attenuated to that token's role
// (owner > operator > viewer) and optional per-island scope — authority only
// ever decreases. There is no token that grants more than the trusted listener
// already does; the token's whole job is to hand a *narrower* credential to an
// automated caller (Scusi, a bot) without sharing the operator's own identity.
//
// A present-but-unknown token is a hard 401 — never silently treated as the
// trusted no-token caller, which would turn a stale/forged token into owner.

// roleCap is the minimum role a route requires. The zero value is capOwner, so a
// route absent from roleRouteCap is owner-only — a new endpoint is locked down by
// default until it is consciously classified (fail-closed, like tokenauth).
type roleCap int

const (
	// capOwner — owner only. The destructive, credential-bearing, and
	// daemon-administration surface (purge, image build, self-update, panic,
	// credential + token management, host terminals, session revoke). Also the
	// default for any unlisted route.
	capOwner roleCap = iota
	// capOperate — operator or owner. Island lifecycle and interaction that is
	// NOT a purge: create, hibernate/wake, reset, upgrade, clone, agent add/
	// remove, exec, file r/w, interactive attach, and Port/capability grants.
	capOperate
	// capRead — any role (viewer, operator, owner). Read + observe: list, status,
	// overview, logs, events, audit, and the non-secret credential listings.
	capRead
)

func (c roleCap) String() string {
	switch c {
	case capRead:
		return "viewer"
	case capOperate:
		return "operator"
	default:
		return "owner"
	}
}

// roleRouteCap maps the exact ServeMux pattern (as registered in routes() and
// RegisterAuth()) to the minimum role that may reach it. Keying on the matched
// *pattern* — not a hand-parsed path — keeps this table in lockstep with the
// router, exactly as tokenauth does. Every current route is listed explicitly so
// the intent is auditable (a test asserts the coverage); anything unlisted falls
// to the capOwner zero value.
var roleRouteCap = map[string]roleCap{
	// --- read + observe (viewer and up) ---
	"GET /v1/healthz":                               capRead,
	"GET /metrics":                                  capRead,
	"GET /v1/islands":                               capRead,
	"GET /v1/islands/{name}":                        capRead,
	"GET /v1/islands/{name}/workspace-ready":        capRead,
	"GET /v1/islands/{name}/agents":                 capRead,
	"GET /v1/islands/{name}/agents/{id}":            capRead,
	"GET /v1/islands/{name}/events":                 capRead,
	"GET /v1/islands/{name}/logs":                   capRead,
	"GET /v1/islands/{name}/port/scopes":            capRead,
	"GET /v1/islands/{name}/github/host-credential": capRead,
	"GET /v1/islands/{name}/capability/grants":      capRead,
	"GET /v1/islands/{name}/mcp/grants":             capRead,
	"GET /v1/islands/{name}/grants":                 capRead, // unified per-island grants view (Lane C)
	"GET /v1/credentials/claude":                    capRead, // status only, no secret
	"GET /v1/credentials/github":                    capRead, // identities, no tokens
	"GET /v1/credentials/github/{name}/repos":       capRead,
	"GET /v1/credentials/providers":                 capRead, // masked, no keys
	"GET /v1/agent-types":                           capRead,
	"GET /v1/events/subscriptions":                  capRead,
	"GET /v1/clients":                               capRead,
	"GET /v1/overview":                              capRead,
	"GET /v1/aggregate":                             capRead, // host-wide rollup (no names); any authed caller
	"GET /v1/audit":                                 capRead,
	"GET /v1/activity":                              capRead, // team activity feed (curated audit view)
	"GET /v1/panic":                                 capRead, // status (engage/clear are owner)

	// --- island lifecycle + interaction (operator and up; never purge) ---
	"POST /v1/islands":                         capOperate, // create (scoped tokens denied — no {name})
	"PATCH /v1/islands/{name}":                 capOperate, // title / no_hibernate
	"POST /v1/islands/{name}/schedules":        capOperate, // add a scheduled wake
	"GET /v1/islands/{name}/schedules":         capOperate, // list scheduled wakes
	"DELETE /v1/islands/{name}/schedules/{id}": capOperate, // remove a scheduled wake
	"PUT /v1/islands/{name}/identity":          capOperate, // visual color+glyph override
	"DELETE /v1/islands/{name}/identity":       capOperate,
	"PUT /v1/islands/{name}/resources":         capOperate,
	"POST /v1/islands/{name}/hibernate":        capOperate,
	"POST /v1/islands/{name}/wake":             capOperate,
	"POST /v1/islands/{name}/reset":            capOperate,
	"POST /v1/islands/{name}/upgrade":          capOperate,
	"POST /v1/islands/{name}/clone":            capOperate,
	"POST /v1/islands/{name}/agents":           capOperate,
	"DELETE /v1/islands/{name}/agents/{id}":    capOperate,
	"PATCH /v1/islands/{name}/agents/{id}":     capOperate,
	// Secrets: names+metadata are readable by any role (values are never
	// returned); writes need operate, and an island token is refused outright in
	// the handler — an agent that can plant a value its peers trust is an
	// escalation path.
	"GET /v1/islands/{name}/secrets":                capRead,
	"PUT /v1/islands/{name}/secrets/{key}":          capOperate,
	"DELETE /v1/islands/{name}/secrets/{key}":       capOperate,
	"POST /v1/islands/{name}/agents/{id}/move":      capOperate,
	"POST /v1/islands/{name}/agents/{id}/restart":   capOperate,
	"PATCH /v1/islands/{name}/agents/{id}/config":   capOperate,
	"PATCH /v1/islands/{name}/egress/policy":        capOperate, // set island egress allow/deny (operator)
	"GET /v1/islands/{name}/spawn-grant":            capRead,    // read the spawn budget (operator/viewer)
	"POST /v1/islands/{name}/spawn-grant":           capOperate, // grant ephemeral-sub-agent spawn budget (operator-only; never an in-island token)
	"DELETE /v1/islands/{name}/spawn-grant":         capOperate, // revoke spawn grant (operator)
	"GET /v1/islands/{name}/session":                capOperate, // interactive attach (control)
	"GET /v1/islands/{name}/shell/session":          capOperate, // in-island contained shell at /workspace
	"GET /v1/islands/{name}/agents/{id}/session":    capOperate,
	"POST /v1/islands/{name}/exec":                  capOperate,
	"GET /v1/islands/{name}/files/{path...}":        capOperate, // reading workspace files is beyond "observe"
	"PUT /v1/islands/{name}/files/{path...}":        capOperate,
	"POST /v1/islands/{name}/port/intake":           capOperate,
	"POST /v1/islands/{name}/port/export":           capOperate,
	"POST /v1/islands/{name}/port/write":            capOperate,
	"POST /v1/islands/{name}/port/scopes":           capOperate, // grant host access (operator act)
	"DELETE /v1/islands/{name}/port/scopes/{scope}": capOperate,
	// Granting the host operator's own gh login is an operator act with the widest
	// blast radius of any grant here — account-wide read of every private repo.
	"POST /v1/islands/{name}/github/host-credential":       capOperate,
	"DELETE /v1/islands/{name}/github/host-credential":     capOperate,
	"POST /v1/islands/{name}/capability/grants":            capOperate,
	"DELETE /v1/islands/{name}/capability/grants/{target}": capOperate,
	"POST /v1/islands/{name}/mcp/grants":                   capOperate, // grant an MCP server (operator act)
	"DELETE /v1/islands/{name}/mcp/grants/{server}":        capOperate,

	// --- owner only (explicit; also the default for anything unlisted) ---
	"DELETE /v1/islands/{name}":                     capOwner, // purge — the operator/owner divide
	"POST /v1/image/build":                          capOwner,
	"GET /v1/local":                                 capOwner,
	"POST /v1/local/install":                        capOwner,
	"GET /v1/local/models":                          capOwner,
	"POST /v1/local/models/{name}/pull":             capOwner,
	"DELETE /v1/local/models/{name}":                capOwner,
	"POST /v1/local/off":                            capOwner,
	"POST /v1/admin/update":                         capOwner,
	"POST /v1/panic":                                capOwner,
	"DELETE /v1/panic":                              capOwner,
	"POST /v1/sessions/revoke":                      capOwner,
	"GET /v1/ssh/account-keys":                      capOwner, // access-control config
	"POST /v1/ssh/account-keys":                     capOwner,
	"PUT /v1/credentials/claude":                    capOwner,
	"PUT /v1/credentials/github/{name}":             capOperate, // self-scoped: an operator pushes only into their OWN tenant (handler enforces); host-owner keeps default/shared/cross-tenant authority
	"DELETE /v1/credentials/github/{name}":          capOperate, // operator deletes only their OWN tenant's identity (handler enforces)
	"POST /v1/credentials/github/device-flow/start": capOperate, // guided sign-in; captures into the caller's own tenant
	"POST /v1/credentials/github/device-flow/poll":  capOperate,
	"PUT /v1/credentials/providers/{provider}":      capOwner,
	"DELETE /v1/credentials/providers/{provider}":   capOwner,
	"POST /v1/events/subscribe":                     capOwner,
	"DELETE /v1/events/subscriptions/{id}":          capOwner,
	"GET /v1/terminals":                             capOwner, // uncontained host shells
	"POST /v1/terminals":                            capOwner,
	"DELETE /v1/terminals/{id}":                     capOwner,
	"PATCH /v1/terminals/{id}":                      capOwner,
	"GET /v1/terminals/{id}/session":                capOwner,
	// Island-listener routes that also exist on the operator mux. On this surface
	// they are owner-only; their real, attenuated home is tokenauth's allow-list.
	"POST /v1/internal/agent-event": capOwner,
	"POST /v1/capabilities/execute": capOwner,
	"POST /v1/mcp/call":             capOwner,
	// Token administration (this lane's own surface) — owner only.
	"POST /v1/tokens":        capOwner,
	"GET /v1/tokens":         capOwner,
	"DELETE /v1/tokens/{id}": capOwner,

	// Lane 5 — inter-island exchange. Granting channels, sending, exposing
	// actions, and approving/denying action delegations are operator acts
	// (owner/operator); listing is read. Approval being capOperate (not capRead)
	// is the Phase-3 invariant: a viewer can watch the queue but never approve,
	// and the requesting/target agents reach none of these (operator listener).
	"GET /v1/links":                                   capRead,
	"POST /v1/links":                                  capOperate,
	"DELETE /v1/links":                                capOperate,
	"POST /v1/islands/{name}/link/send":               capOperate,
	"GET /v1/islands/{name}/link/actions":             capRead,
	"PUT /v1/islands/{name}/link/actions/{action}":    capOperate,
	"DELETE /v1/islands/{name}/link/actions/{action}": capOperate,
	"POST /v1/islands/{name}/link/action":             capOperate,
	"GET /v1/link/actions":                            capRead,
	"GET /v1/link/actions/watch":                      capRead, // stream the queue (viewer may watch, not approve)
	"POST /v1/link/actions/{id}/approve":              capOperate,
	"POST /v1/link/actions/{id}/deny":                 capOperate,
	// Auto-approve policy is operator-managed end to end — even listing rules is
	// privileged (a rule is a standing bypass; adding/removing one is sensitive).
	"GET /v1/policy":    capOperate,
	"POST /v1/policy":   capOperate,
	"DELETE /v1/policy": capOperate,
}

// identityKey carries the resolved Identity down to handlers and to Lane 1's
// audit log (which attributes each record to who/role).
type identityKey struct{}

// WithIdentity returns ctx carrying id. Set by roleAuth on every operator
// request, so IdentityFromContext is populated for all inner handlers/middleware.
func WithIdentity(ctx context.Context, id authtoken.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns the authenticated identity for an operator-surface
// request, and ok=false for requests that did not pass through roleAuth (e.g.
// the in-island token listener, whose actor is TokenIslandFromContext instead).
func IdentityFromContext(ctx context.Context) (authtoken.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(authtoken.Identity)
	return id, ok
}

// trustedOwner is the identity for a no-token request on a trusted listener: the
// caller the unix socket / tailnet pinning already vouches for, running with
// full authority. Distinct from a minted owner token only by Subject + empty id.
func trustedOwner() authtoken.Identity {
	// The trusted local caller acts AS the host owner tenant, so islands it creates
	// are attributed to HostOwner (and it sees all via OwnsAll regardless).
	return authtoken.Identity{Subject: "local", Role: authtoken.RoleOwner, Owner: project.HostOwner()}
}

// ownerOf resolves an island's owner tenant for the authorization layer. The
// bool is false when the island can't be loaded (unknown/malformed) — the owner
// gate then defers to the handler (which 404s), rather than leaking existence.
func (s *Server) ownerOf(island string) (string, bool) {
	p, err := project.Load(island)
	if err != nil {
		return "", false
	}
	return p.Owner, true
}

// visibleTo reports whether an island belongs in the request caller's own-fleet
// view (private visibility, P2). The host owner — RoleOwner, or a no-identity
// caller on the trusted surface — sees all; a teammate sees only islands its
// tenant owns. Used to filter listIslands + the overview aggregate.
func (s *Server) visibleTo(ctx context.Context, p *project.Project) bool {
	id, ok := IdentityFromContext(ctx)
	if !ok || id.OwnsAll() {
		return true
	}
	return p.Owner == id.Owner
}

// RequireToken makes the operator surface reject anonymous (no-token) requests
// with 401, turning bearer tokens into a hard boundary rather than opt-in
// attenuation. Off by default: the trusted listeners (unix socket, tailnet) keep
// working without a token. Turn it on when the daemon is reached by callers that
// should hold only an attenuated service token (e.g. an off-tailnet control
// plane). Set from dejimad via the --require-token flag.
func (s *Server) RequireToken() { s.requireToken = true }

// roleAuth authenticates the optional bearer token, attenuates the request to
// that token's role + island scope, puts the resolved Identity on the context,
// and dispatches to next. It classifies on mux (the route *pattern*) but serves
// next, so an audit/logging stage can sit between auth and the mux:
// authenticate → next (e.g. audit) → handle. Today Handler passes next == mux;
// Lane 1's audit follow-up passes next = auditMiddleware(mux). It wraps
// Server.Handler()'s mux — never the in-island token listener.
func (s *Server) roleAuth(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		var id authtoken.Identity
		switch tok {
		case "":
			if s.requireToken {
				writeError(w, http.StatusUnauthorized,
					errors.New("authentication required: present an Authorization: Bearer <token>"))
				return
			}
			id = trustedOwner()
		default:
			resolved, ok := authtoken.Resolve(tok)
			if !ok {
				// Present but unknown — a hard auth failure. Never fall through to
				// the trusted-owner default, which would promote a stale or forged
				// token to full authority.
				writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
				return
			}
			id = resolved
		}

		// Classify on the pattern the router will actually dispatch to, so the
		// authorization decision can never diverge from routing. An empty pattern
		// (no route matched) is left to the mux to answer 404/405. Authorize on the
		// *escaped* path so an encoded slash in {name} can't be parsed as a
		// different island than the router binds (see islandFromPath).
		if _, pattern := mux.Handler(r); pattern != "" {
			if err := authorizeRole(id, pattern, r.URL.EscapedPath(), s.ownerOf); err != nil {
				s.log.Warn("role request denied",
					"subject", id.Subject, "role", id.Role,
					"method", r.Method, "path", r.URL.Path, "reason", err)
				writeError(w, http.StatusForbidden, err)
				return
			}
		}

		// Stamp both the team-auth Identity (for handlers) and Lane 1's
		// AuditIdentity (for the audit middleware in `next`), so a record is
		// attributed to who/role. authenticate → audit → handle.
		ctx := WithIdentity(r.Context(), id)
		ctx = WithAuditIdentity(ctx, AuditIdentity{Actor: id.Subject, Role: string(id.Role)})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authorizeRole decides whether identity id may take the request matched to
// pattern (escapedPath is r.URL.EscapedPath()). Default-deny via the capOwner
// zero value for unlisted routes.
// authorizeRole decides whether an identity may take a request. Two orthogonal
// dimensions compose: (1) the role capability + optional static --island scope
// (unchanged), and (2) multi-tenant OWNER scope — a non-owner may only touch
// islands it owns. ownerOf resolves an island's owner ("" + false when unknown);
// it's injected so the policy stays a pure, testable function.
func authorizeRole(id authtoken.Identity, pattern, escapedPath string, ownerOf func(string) (string, bool)) error {
	need := roleRouteCap[pattern]
	if !roleAllows(id.Role, need) {
		return fmt.Errorf("role %q may not access this route (requires %s)", id.Role, need)
	}
	islandScoped := strings.Contains(pattern, "/{name}")

	// (1) Static island scope. A token with an explicit --island list may act only
	// on those islands ({name} routes) and never mutate/administer globally. A
	// token WITHOUT a static list (the owner-scoped teammate case) is unrestricted
	// here — its access is bounded by (2) instead, which is what lets it CREATE.
	if id.Scoped() {
		if islandScoped {
			name, ok := islandFromPath(escapedPath)
			if !ok {
				return errors.New("invalid or unparseable target island for a scoped token")
			}
			if !id.MayTouch(name) {
				return fmt.Errorf("token is scoped to islands %v; %q is out of scope", id.Islands, name)
			}
		} else if need != capRead {
			return errors.New("token is island-scoped and cannot perform global operations")
		}
	}

	// (2) Owner scope. The host owner (RoleOwner) sees/does all. A non-owner may
	// only touch an island it owns; create + other global routes are not gated
	// here (create is allowed and the new island is stamped to the caller — see
	// createIsland — and other globals are role/scope-bounded above).
	if !id.OwnsAll() && islandScoped {
		name, ok := islandFromPath(escapedPath)
		if !ok {
			return errors.New("invalid or unparseable target island")
		}
		if owner, known := ownerOf(name); known && owner != id.Owner {
			return fmt.Errorf("island %q is not yours", name)
		}
	}
	return nil
}

// roleAllows reports whether role satisfies the route's minimum capability.
func roleAllows(role authtoken.Role, need roleCap) bool {
	switch need {
	case capRead:
		return role == authtoken.RoleOwner || role == authtoken.RoleOperator || role == authtoken.RoleViewer
	case capOperate:
		return role == authtoken.RoleOwner || role == authtoken.RoleOperator
	case capOwner:
		return role == authtoken.RoleOwner
	default:
		return false
	}
}
