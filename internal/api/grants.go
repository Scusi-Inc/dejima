package api

import (
	"context"
	"net/http"

	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/project"
)

// grants.go is the unified per-island grants view (Lane C / P0.3). The four
// grant surfaces — Port host-folder scopes, host capabilities, MCP servers, and
// inter-island link channels — already each expose their own operator-readable
// list endpoint (handleListPortScopes, handleListCapabilityGrants,
// handleListMCPGrants, listLinks). This file adds nothing to those stores; it
// stitches their *view builders* into a single response so a consumer (the TUI
// grants view, frontend P1.8) reads every grant an island holds in one call
// instead of fanning out to four.
//
// Operator-readable (capRead in roleauth.go), exactly like the four it
// aggregates. It owns no grant state: the source of truth stays in port.go /
// capability.go / mcp.go / link.go.

// IslandGrantsResponse is the body of GET /v1/islands/{name}/grants — every
// grant type an island holds, in one typed shape. The per-type fields mirror the
// element types of the existing list endpoints (PortScopesResponse.Scopes,
// CapabilityGrantsResponse.Grants, MCPGrantsResponse.Grants, LinksResponse.Grants),
// so a consumer that already knows those shapes needs no new types. Each slice is
// non-nil (empty, never null) so a fresh deny-all island serializes as empty
// arrays rather than nulls. Links are filtered to the channels that touch this
// island (as From or To).
type IslandGrantsResponse struct {
	Port       []PortScopeView       `json:"port"`
	Capability []CapabilityGrantView `json:"capability"`
	MCP        []MCPGrantView        `json:"mcp"`
	Links      []link.Grant          `json:"links"`
	// HostGitHub is the host-operator gh credential grant. Unlike the four
	// above it is a single yes/no rather than a list, and it is the one grant
	// that used to be implicit — it appears here so "what does this island
	// hold" has one answer instead of four plus a special case.
	HostGitHub HostGitHubCredentialView `json:"host_github"`
}

// handleListGrants returns every grant type for one island in a single shape.
// Operator-readable. It reuses the existing handlers' view builders and stores —
// project state for Port/capability/MCP, the link store for channels — and never
// mutates anything. A fresh island returns all-empty (deny-all is genuine: no
// grant type carries a convenience default).
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	// MCP grants live outside the project file (project.MCPGrantsFor), like the
	// dedicated handler reads them.
	mcpGrants, err := project.MCPGrantsFor(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Link grants are global; filter to the channels that touch this island.
	st, err := link.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	links := linksTouching(st.List(), name)

	writeJSON(w, http.StatusOK, IslandGrantsResponse{
		Port:       scopeViews(p.Ports),
		Capability: capabilityViews(p.Capabilities),
		MCP:        mcpGrantViews(mcpGrants),
		Links:      links,
		HostGitHub: hostGitHubView(p),
	})
}

// linksTouching returns the link grants where island is either endpoint
// (From or To). Always non-nil so the field serializes as [] not null.
func linksTouching(grants []link.Grant, island string) []link.Grant {
	out := make([]link.Grant, 0, len(grants))
	for _, g := range grants {
		if g.From == island || g.To == island {
			out = append(out, g)
		}
	}
	return out
}

// ListGrants returns every grant type an island holds (Port scopes, capability
// targets, MCP servers, and the inter-island link channels touching it) in one
// call — the unified view the operator surface exposes at
// GET /v1/islands/{name}/grants.
func (c *Client) ListGrants(ctx context.Context, name string) (*IslandGrantsResponse, error) {
	var out IslandGrantsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/grants", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
