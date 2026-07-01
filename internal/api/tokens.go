package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/authtoken"
	"github.com/aoos/dejima/internal/project"
)

// This file is the team-auth token-administration surface: issue, list, and
// revoke the operator bearer tokens that carry the role/scope model enforced in
// roleauth.go. Every route here is owner-only (classified capOwner in
// roleRouteCap) — minting credentials is the most authority-bearing operator
// act, so it never delegates below owner.

// TokenView is the public, secret-free view of an issued token.
type TokenView struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	Role      string    `json:"role"`
	Owner     string    `json:"owner,omitempty"`   // tenant this token acts as (multi-tenant ownership)
	Islands   []string  `json:"islands,omitempty"` // scope; empty = all the owner's islands
	CreatedAt time.Time `json:"created_at"`
}

func tokenView(t authtoken.Token) TokenView {
	return TokenView{
		ID:        t.ID,
		Label:     t.Label,
		Role:      string(t.Role),
		Owner:     t.Owner,
		Islands:   t.Islands,
		CreatedAt: t.CreatedAt,
	}
}

// CreateTokenRequest is the body of POST /v1/tokens.
type CreateTokenRequest struct {
	Label string `json:"label,omitempty"`
	// Role is "owner", "operator", or "viewer".
	Role string `json:"role"`
	// Owner scopes the token to a tenant: an operator/viewer token sees + acts on
	// only the islands that tenant owns (and islands it creates become the
	// tenant's). Empty defaults to the host owner — i.e. an unscoped operator token
	// behaves as before (full access to the host operator's fleet). A RoleOwner
	// token is the host admin and sees all regardless. Owner-only to set (this
	// whole surface is capOwner).
	Owner string `json:"owner,omitempty"`
	// Islands optionally further narrows the token to specific island names; empty
	// grants it across all the owner's islands (still bounded by Role + Owner).
	Islands []string `json:"islands,omitempty"`
}

// CreateTokenResponse carries the new token's metadata plus its bearer secret —
// the ONLY time the secret is ever returned. It is not stored in the clear, so a
// lost secret means minting a new token, not recovering this one.
type CreateTokenResponse struct {
	Token  TokenView `json:"token"`
	Secret string    `json:"secret"`
}

// TokensResponse is the body of GET /v1/tokens.
type TokensResponse struct {
	Tokens []TokenView `json:"tokens"`
}

// RegisterAuth registers the team-auth token-administration routes on mux. Called
// once from routes() (one append-only line per the lane seam contract). The
// owner-only gate is enforced by roleAuth via roleRouteCap, not re-checked here.
func (s *Server) RegisterAuth(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /v1/tokens", s.handleListTokens)
	mux.HandleFunc("DELETE /v1/tokens/{id}", s.handleRevokeToken)
}

// handleCreateToken mints a new operator bearer token and returns its secret
// once. Owner-only (roleRouteCap).
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req CreateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	role := authtoken.Role(strings.TrimSpace(req.Role))
	if !authtoken.ValidRole(role) {
		writeError(w, http.StatusBadRequest,
			errors.New(`role is required and must be one of "owner", "operator", or "viewer"`))
		return
	}
	// Empty owner defaults to the host owner: an unscoped operator token then
	// behaves as before (full access to the host operator's fleet). Scope a
	// teammate by passing an explicit owner.
	owner := strings.TrimSpace(req.Owner)
	if owner == "" {
		owner = project.HostOwner()
	}
	secret, tok, err := authtoken.Create(req.Label, role, req.Islands, owner)
	if err != nil {
		// A bad island name in the scope is the caller's mistake (400); anything
		// else is a store failure (500).
		if strings.Contains(err.Error(), "invalid island") {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Attribute the mint in the daemon log (who/role comes from the caller's
	// identity); never log the secret.
	actor := "local"
	if id, ok := IdentityFromContext(r.Context()); ok {
		actor = id.Subject
	}
	s.log.Info("operator token issued",
		"id", tok.ID, "role", tok.Role, "islands", tok.Islands, "by", actor)
	writeJSON(w, http.StatusCreated, CreateTokenResponse{Token: tokenView(tok), Secret: secret})
}

// handleListTokens returns every issued token's metadata (never a secret).
// Owner-only (roleRouteCap).
func (s *Server) handleListTokens(w http.ResponseWriter, _ *http.Request) {
	toks, err := authtoken.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := TokensResponse{Tokens: make([]TokenView, 0, len(toks))}
	for _, t := range toks {
		out.Tokens = append(out.Tokens, tokenView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeToken deletes a token by id. Owner-only (roleRouteCap).
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	removed, err := authtoken.Revoke(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such token %q", id))
		return
	}
	actor := "local"
	if ident, ok := IdentityFromContext(r.Context()); ok {
		actor = ident.Subject
	}
	s.log.Info("operator token revoked", "id", id, "by", actor)
	w.WriteHeader(http.StatusNoContent)
}
