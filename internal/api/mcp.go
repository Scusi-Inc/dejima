package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/mcpbroker"
	"github.com/aoos/dejima/internal/project"
)

// mcp.go is the audited MCP broker (build-queue #3): deny-by-default grants of
// named, host-curated MCP servers into an island, plus a brokered call path that
// writes an `mcp.*` Ledger entry for every outcome. It is the Port/capability
// pattern (deny-all, per-island, typed, ledgered) applied to MCP servers — see
// docs/mcp-broker-spec.md. The grant routes are operator-only (registered here,
// absent from tokenauth's allow-list, so a contained brain can never self-grant);
// the call route is reached over the trusted control plane in V1 (the in-island
// token path is a one-line follow-up in tokenauth.go, owned by the team-auth lane).

// maxMCPParamsBytes bounds a call's JSON-RPC params payload — keeps the wire, the
// adapter input, and the (hashed) ledger record sane.
const maxMCPParamsBytes = 64 << 10

// newMCPBroker resolves the host's MCP broker. A package var so tests inject a
// fake without a Server field (and without touching server.go beyond the single
// RegisterMCP seam). nil result is impossible; an error means this host can't
// broker MCP (surfaced as 503).
var newMCPBroker = mcpbroker.Default

// RegisterMCP mounts the MCP-broker routes. Called once from server.go's
// routes() — the single shared-file seam this lane adds (append-only). Keeping
// the registrations here means the route surface lives beside its handlers.
func (s *Server) RegisterMCP(mux routeMux) {
	// Grant surface — operator-only (absent from tokenRouteAccess).
	mux.HandleFunc("GET /v1/islands/{name}/mcp/grants", s.handleListMCPGrants)
	mux.HandleFunc("POST /v1/islands/{name}/mcp/grants", s.handleGrantMCP)
	mux.HandleFunc("DELETE /v1/islands/{name}/mcp/grants/{server}", s.handleRevokeMCP)
	// Brokered call — operator/control-plane in V1; island named in the body.
	mux.HandleFunc("POST /v1/mcp/call", s.handleMCPCall)
}

// handleListMCPGrants returns the MCP servers an island may invoke.
func (s *Server) handleListMCPGrants(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	grants, err := project.MCPGrantsFor(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, MCPGrantsResponse{Grants: mcpGrantViews(grants)})
}

// handleGrantMCP grants an island permission to invoke a named host MCP server.
// The server's presence in the host registry is not required at grant time — a
// grant for a not-yet-registered server is recorded and fails closed at call
// time until the operator adds it (grant-ahead, like capabilities).
func (s *Server) handleGrantMCP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req MCPGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := mcpbroker.ValidateServerName(req.Server); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	grant, err := project.AddMCPGrant(name, project.MCPGrant{Server: req.Server, GrantedAt: time.Now().UTC()})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "mcp.grant", Island: name, Scope: grant.Server, Decision: "allowed",
	})
	writeJSON(w, http.StatusCreated, mcpGrantView(grant))
}

// handleRevokeMCP drops an MCP-server grant by name.
func (s *Server) handleRevokeMCP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	server := r.PathValue("server")
	removed, ok, err := project.RemoveMCPGrant(name, server)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no mcp grant %q", name, server))
		return
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "mcp.revoke", Island: name, Scope: removed.Server, Decision: "allowed",
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleMCPCall brokers one JSON-RPC call to a granted MCP server. Deny-all: the
// island must hold a grant for the server, and the method must be in the brokered
// surface. The island is taken from the bearer token when present (forward-compat
// with the in-island path), else from the body (the operator/control-plane V1
// path). Every outcome — allowed or denied — is ledgered.
func (s *Server) handleMCPCall(w http.ResponseWriter, r *http.Request) {
	var req MCPCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// A token (the in-island brain, once the token path is wired) pins the island;
	// an operator names it. The token value can never be overridden by the body.
	island := TokenIslandFromContext(r.Context())
	if island == "" {
		island = req.Island
	}
	if island == "" {
		writeError(w, http.StatusBadRequest, errors.New("island is required"))
		return
	}
	if err := mcpbroker.ValidateServerName(req.Server); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !mcpbroker.MethodAllowed(req.Method) {
		s.ledgerMCPDeny(island, req.Server, "method not permitted")
		writeError(w, http.StatusBadRequest, fmt.Errorf("mcp method %q is not permitted by the broker", req.Method))
		return
	}
	if len(req.Params) > maxMCPParamsBytes {
		writeError(w, http.StatusBadRequest, fmt.Errorf("params exceed %d bytes", maxMCPParamsBytes))
		return
	}

	if _, err := project.Load(island); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// Deny-all: no grant ⇒ refuse, ledgered.
	if _, ok, err := project.MCPGrantByServer(island, req.Server); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		s.ledgerMCPDeny(island, req.Server, "not granted")
		writeError(w, http.StatusForbidden, fmt.Errorf("mcp server %q is not granted to island %q", req.Server, island))
		return
	}

	broker, err := newMCPBroker()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("mcp brokering unavailable on this host: %w", err))
		return
	}
	res, callErr := broker.Call(r.Context(), mcpbroker.Request{
		Island: island, Server: req.Server, Method: req.Method, Params: req.Params,
	})
	if callErr != nil {
		status, reason := classifyMCPError(callErr)
		s.ledgerMCPDeny(island, req.Server, reason)
		writeError(w, status, callErr)
		return
	}

	var seq uint64
	if lg, lerr := ledger.Default(); lerr == nil {
		if e, aerr := lg.Append(ledger.Entry{
			Type: "mcp.call", Island: island, Scope: req.Server,
			Detail:   mcpCallDetail(req.Method, req.Params, res.IsError),
			SHA256:   mcpParamsHash(req.Params),
			Decision: "allowed",
		}); aerr == nil {
			seq = e.Seq
		}
	}
	writeJSON(w, http.StatusOK, MCPCallResponse{
		OK: !res.IsError, IsError: res.IsError, Result: res.Output, LedgerSeq: seq,
	})
}

// ledgerMCPDeny records a refused or failed call (best-effort).
func (s *Server) ledgerMCPDeny(island, server, reason string) {
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "mcp.deny", Island: island, Scope: server, Decision: "denied", Detail: reason,
	})
}

// classifyMCPError maps a broker error to an HTTP status + a short ledger reason.
// A tools/call that ran and returned isError:true is NOT an error (it returns a
// Result), so it never reaches here.
func classifyMCPError(err error) (int, string) {
	switch {
	case errors.Is(err, mcpbroker.ErrServerNotFound):
		return http.StatusNotFound, "server not in registry"
	case errors.Is(err, mcpbroker.ErrServerUntrusted):
		return http.StatusForbidden, "registry untrusted"
	case errors.Is(err, mcpbroker.ErrMethodNotAllowed):
		return http.StatusBadRequest, "method not permitted"
	case errors.Is(err, mcpbroker.ErrTimeout):
		return http.StatusGatewayTimeout, "timeout"
	case errors.Is(err, mcpbroker.ErrProtocol):
		return http.StatusBadGateway, "protocol error"
	default:
		return http.StatusUnprocessableEntity, "call failed"
	}
}

// mcpCallDetail is the human-readable ledger Detail for a call: the method, the
// tool name for tools/call, and whether the result was an application error.
func mcpCallDetail(method string, params json.RawMessage, isErr bool) string {
	detail := method
	if method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(params, &p) == nil && p.Name != "" {
			detail += " name=" + p.Name
		}
	}
	if isErr {
		detail += " isError"
	}
	return detail
}

// mcpParamsHash is a canonical SHA-256 of the JSON-RPC params — what the ledger
// records instead of the raw params (tractable + PII-free, like a file trade's
// content hash or a capability's args hash). Canonicalized by a round-trip so
// key order / whitespace don't change the hash; empty for no params.
func mcpParamsHash(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	canonical := params
	var v any
	if json.Unmarshal(params, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canonical = b
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func mcpGrantView(g project.MCPGrant) MCPGrantView {
	return MCPGrantView{Server: g.Server, GrantedAt: g.GrantedAt}
}

func mcpGrantViews(gs []project.MCPGrant) []MCPGrantView {
	out := make([]MCPGrantView, 0, len(gs))
	for _, g := range gs {
		out = append(out, mcpGrantView(g))
	}
	return out
}

// --- API types (kept in this file, not the shared types.go, to stay collision-
// free with the other lanes) ----------------------------------------------------

// MCPGrantRequest is the body of POST /v1/islands/:name/mcp/grants — grant the
// island permission to invoke a named host MCP server.
type MCPGrantRequest struct {
	Server string `json:"server"`
}

// MCPGrantView is one MCP-server grant as returned by the API.
type MCPGrantView struct {
	Server    string    `json:"server"`
	GrantedAt time.Time `json:"granted_at"`
}

// MCPGrantsResponse is the body of GET /v1/islands/:name/mcp/grants.
type MCPGrantsResponse struct {
	Grants []MCPGrantView `json:"grants"`
}

// MCPCallRequest is the body of POST /v1/mcp/call. Island is supplied by an
// operator caller; a token-authenticated in-island caller (once that path is
// wired) is pinned to its own island and Island is ignored. Method must be in
// the brokered surface (mcpbroker.AllowedMethods); Params is the JSON-RPC params.
type MCPCallRequest struct {
	Island string          `json:"island,omitempty"`
	Server string          `json:"server"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// MCPCallResponse is the result of a brokered MCP call. OK reports protocol
// success with no application error; IsError reports a tools/call that completed
// with isError:true (the call ran — the tool reported a problem). Result is the
// raw JSON-RPC result.
type MCPCallResponse struct {
	OK        bool            `json:"ok"`
	IsError   bool            `json:"is_error,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	LedgerSeq uint64          `json:"ledger_seq,omitempty"`
}

// --- Client methods -------------------------------------------------------------

// ListMCPGrants returns the MCP servers an island may invoke.
func (c *Client) ListMCPGrants(ctx context.Context, name string) (*MCPGrantsResponse, error) {
	var out MCPGrantsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/mcp/grants", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GrantMCP grants an island permission to invoke a named host MCP server.
func (c *Client) GrantMCP(ctx context.Context, name, server string) (*MCPGrantView, error) {
	var out MCPGrantView
	req := MCPGrantRequest{Server: server}
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/mcp/grants", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeMCP drops an MCP-server grant by name.
func (c *Client) RevokeMCP(ctx context.Context, name, server string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+name+"/mcp/grants/"+url.PathEscape(server), nil, nil)
}

// CallMCP brokers one JSON-RPC call to a granted MCP server.
func (c *Client) CallMCP(ctx context.Context, req MCPCallRequest) (*MCPCallResponse, error) {
	var out MCPCallResponse
	if err := c.do(ctx, http.MethodPost, "/v1/mcp/call", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
