package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// Operator surface for the host-gh credential grant (see
// project/github_host.go). Shaped like the Port scope endpoints — load, mutate,
// save, ledger — because this IS one of the brokered grants now, and the point
// of the change is that it stops being the one credential that behaves
// differently from all the others.

// HostGitHubCredentialView is the operator-readable state of the grant.
type HostGitHubCredentialView struct {
	// Granted is the answer to "can this island use the host operator's login".
	Granted   bool      `json:"granted"`
	GrantedAt time.Time `json:"granted_at,omitempty"`
	GrantedBy string    `json:"granted_by,omitempty"`
	// Grandfathered is true for a grant the deny-by-default migration wrote to
	// preserve an island's existing access. It means "nobody has actually decided
	// about this island yet" and is the flag a surface should nag on.
	Grandfathered bool `json:"grandfathered,omitempty"`
	// Eligible is false for a tenant island, where this grant does not apply at
	// all: tenants use a per-island GitHub identity and never the host's login.
	Eligible bool `json:"eligible"`
}

// hostGitHubView builds the view for one island.
func hostGitHubView(p *project.Project) HostGitHubCredentialView {
	v := HostGitHubCredentialView{Eligible: p.IsHostOwned()}
	if g := p.HostGitHubGrant; g != nil {
		v.Granted, v.GrantedAt, v.GrantedBy, v.Grandfathered = true, g.GrantedAt, g.GrantedBy, g.Grandfathered
	}
	return v
}

// handleGetHostGitHubCredential reports whether an island may use the host
// operator's own gh login.
func (s *Server) handleGetHostGitHubCredential(w http.ResponseWriter, r *http.Request) {
	p, err := project.Load(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, hostGitHubView(p))
}

// handleGrantHostGitHubCredential grants an island the host operator's own gh
// login. Refused for a tenant island: tenants resolve a per-island identity and
// must never fall back to the host's account.
//
// The grant takes effect when the container is next created (the credential is
// a bind mount), so the response says so rather than implying it is live.
func (s *Server) handleGrantHostGitHubCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !p.IsHostOwned() {
		writeError(w, http.StatusBadRequest, fmt.Errorf(
			"island %q belongs to tenant %q, which cannot use the host operator's GitHub login — "+
				"give it its own identity instead (see docs/github-identities.md)", name, p.Owner))
		return
	}
	actor := ""
	if id, ok := IdentityFromContext(r.Context()); ok {
		actor = id.Subject
	}
	p.GrantHostGitHub(actor, time.Now())
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "github.host-credential.grant", Island: name, Decision: "allowed",
		Actor: actor, Detail: "island may use the host operator's gh login (account-wide read)",
	})
	writeJSON(w, http.StatusCreated, hostGitHubView(p))
}

// handleRevokeHostGitHubCredential drops the grant. Idempotent-ish: a 404 tells
// the operator there was nothing to revoke, which is a different fact from
// "revoked", and worth distinguishing when auditing a fleet.
func (s *Server) handleRevokeHostGitHubCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	removed, ok := p.RevokeHostGitHub()
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q holds no host GitHub credential grant", name))
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	actor := ""
	if id, ok := IdentityFromContext(r.Context()); ok {
		actor = id.Subject
	}
	detail := "operator grant revoked"
	if removed.Grandfathered {
		detail = "grandfathered grant revoked (island no longer inherits the host login)"
	}
	s.ledgerAppend(ledger.Entry{
		Type: "github.host-credential.revoke", Island: name, Decision: "allowed",
		Actor: actor, Detail: detail,
	})
	w.WriteHeader(http.StatusNoContent)
}

// GetHostGitHubCredential reports whether an island may use the host operator's
// own GitHub login.
func (c *Client) GetHostGitHubCredential(ctx context.Context, island string) (*HostGitHubCredentialView, error) {
	var out HostGitHubCredentialView
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+island+"/github/host-credential", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GrantHostGitHubCredential lets an island use the host operator's own GitHub
// login. Takes effect on the island's next container create.
func (c *Client) GrantHostGitHubCredential(ctx context.Context, island string) (*HostGitHubCredentialView, error) {
	var out HostGitHubCredentialView
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+island+"/github/host-credential", struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeHostGitHubCredential removes the grant.
func (c *Client) RevokeHostGitHubCredential(ctx context.Context, island string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+island+"/github/host-credential", nil, nil)
}
