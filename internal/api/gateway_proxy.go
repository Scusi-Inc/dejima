package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// The gateway proxy: GET|POST|PUT|DELETE /v1/islands/{name}/agents/{id}/gateway/{path...}
//
// The daemon relays bytes to a framework's console inside the island. It does
// NOT model that framework's API — see docs/gateway-proxy.md; opaque was chosen
// deliberately, so letta and goose work the day they declare a GatewayPort and
// OpenClaw's churn never becomes our bug.
//
// Three problems are solved by one route:
//
//   - REACHABILITY. `agent open` tunnels from the machine running the CLI. A
//     browser is not that machine and cannot run `ssh -L`, so publishing a URL
//     helps only a client that could already dial it.
//   - THE CREDENTIAL. handlers.go pins a gateway auth token the daemon never
//     shares. Injected here, server-side, the client never holds it — strictly
//     better than returning it on AgentInfo, which would put a credential in a
//     browser.
//   - READINESS. One implementation both clients read, instead of each
//     reimplementing gatewayReady and drifting.

// gatewayProxyPrefix is the path segment after which everything belongs to the
// gateway rather than to us.
const gatewayProxyPrefix = "/gateway"

// gatewayIdleTimeout bounds an idle upstream connection.
//
// Deliberately NOT defaultDockerTimeout. That budget exists to stop a wedged
// engine command from hanging a request; applying it here would kill a console's
// live connection a few seconds after it went quiet, which is precisely the
// "Gateway connection lost" symptom this route is supposed to end. A console
// holds a socket open across a user's thinking time.
const gatewayIdleTimeout = 10 * time.Minute

// gatewayDialTimeout bounds getting INTO the island, which is a `docker exec`
// and should be quick. Separate from the idle timeout above on purpose: a slow
// dial is a broken island, a quiet connection is a thinking user.
const gatewayDialTimeout = 10 * time.Second

// handleAgentGateway proxies a request to the agent's framework gateway.
func (s *Server) handleAgentGateway(w http.ResponseWriter, r *http.Request) {
	name, agentID, rest, ok := parseGatewayPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("not a gateway path"))
		return
	}
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	a := findAgent(p, agentID)
	if a == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, agentID))
		return
	}
	h, known := handlers.Lookup(a.Type)
	if !known || h.GatewayPort == 0 {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("agent type %q has no localhost gateway to proxy (it may be CLI- or messaging-only)", a.Type))
		return
	}

	// One ledger entry per session opened, not per request. A console makes
	// hundreds of requests for one operator action; per-request entries would
	// bury every other ledger line under noise, and this is the operator reaching
	// their OWN island — the direction that does not need semantic auditing.
	// "Session" here means an upgrade (a websocket) or the first request on a
	// fresh upstream connection; see gatewayConnTracker.
	s.ledgerGatewayOpen(name, agentID, r)

	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", h.GatewayPort)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = rest
			pr.Out.Host = target.Host
			// The pinned console token, injected server-side. The client never
			// sees it and cannot: that is the point of proxying rather than
			// publishing the address.
			if tok := s.gatewayToken(r.Context(), p, a, h); tok != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+tok)
			}
			// X-Forwarded-For is set by SetURL; drop the rest of our own hop's
			// headers so the gateway sees a clean request.
			pr.Out.Header.Del("Authorization-Dejima")
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dctx, cancel := context.WithTimeout(ctx, gatewayDialTimeout)
				defer cancel()
				return s.rt.DialContainerPort(dctx, p.ContainerName(), "127.0.0.1", h.GatewayPort)
			},
			// No response header timeout and no global response timeout: a
			// streaming console legitimately holds one open.
			IdleConnTimeout:       gatewayIdleTimeout,
			ResponseHeaderTimeout: gatewayDialTimeout,
			DisableCompression:    true,
		},
		// FlushInterval -1 means flush immediately after every write, which is
		// what a streaming response needs. Buffering a console's event stream
		// would make it arrive in silence and then all at once.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// 502, not 500: the failure is upstream, and saying so keeps a
			// gateway that has not started yet from reading as a daemon fault.
			writeError(w, http.StatusBadGateway,
				fmt.Errorf("couldn't reach %s's gateway in %s: %w", agentID, name, err))
		},
	}
	proxy.ServeHTTP(w, r)
}

// parseGatewayPath splits /v1/islands/{name}/agents/{id}/gateway/{rest...}.
// rest always begins with "/" so it can be used as an upstream path directly.
func parseGatewayPath(p string) (island, agent, rest string, ok bool) {
	const root = "/v1/islands/"
	if !strings.HasPrefix(p, root) {
		return "", "", "", false
	}
	tail := p[len(root):]
	i := strings.Index(tail, "/agents/")
	if i <= 0 {
		return "", "", "", false
	}
	island = tail[:i]
	tail = tail[i+len("/agents/"):]
	j := strings.Index(tail, gatewayProxyPrefix)
	if j <= 0 {
		return "", "", "", false
	}
	agent = tail[:j]
	rest = tail[j+len(gatewayProxyPrefix):]
	if rest == "" {
		rest = "/"
	}
	if strings.Contains(island, "/") || strings.Contains(agent, "/") {
		return "", "", "", false
	}
	return island, agent, rest, true
}

func findAgent(p *project.Project, id string) *project.AgentSpec {
	for i := range p.Agents {
		if p.Agents[i].ID == id {
			return &p.Agents[i]
		}
	}
	return nil
}

// gatewayToken reads the framework's pinned console token from inside the
// container. Best-effort: a gateway with no token configured needs none, and a
// container we cannot exec into will fail the proxy dial anyway with a better
// message than a token error would give.
func (s *Server) gatewayToken(ctx context.Context, p *project.Project, a *project.AgentSpec, h handlers.Handler) string {
	if h.DashboardTokenCmd == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayDialTimeout)
	defer cancel()
	out, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"bash", "-lc", h.DashboardTokenCmd})
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// ledgerGatewayOpen records one console session. Upgrades are always recorded;
// ordinary requests are not, because a console issues hundreds per action.
func (s *Server) ledgerGatewayOpen(island, agentID string, r *http.Request) {
	if !isUpgradeRequest(r) {
		return
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type:     "gateway.session",
		Island:   island,
		Detail:   "console session opened for agent " + agentID,
		Decision: "allowed",
	})
}

// isUpgradeRequest reports whether this request asks to leave HTTP behind — a
// websocket, in practice. Checked case-insensitively and as a comma list,
// because "Connection: keep-alive, Upgrade" is legal and common.
func isUpgradeRequest(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}
