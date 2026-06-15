package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// capability.go is the host-side capability broker's grant surface (Phase 1):
// grant / revoke / list the named host actions an island may invoke. Execution
// (POST /v1/capabilities/execute) and the adapters land in later phases; until
// then a grant is a recorded, ledgered permission with no runtime effect.
//
// Grants are operator-only — these routes are not in tokenRouteAccess, so the
// in-island token path can never reach them (a contained brain can never grant
// itself a capability), exactly as with Port scope grants.

// handleListCapabilityGrants returns the capability targets an island may invoke.
func (s *Server) handleListCapabilityGrants(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, CapabilityGrantsResponse{Grants: capabilityViews(p.Capabilities)})
}

// handleGrantCapability grants an island permission to invoke a named host
// capability target (a macOS Shortcut, or a ~/.dejima/capabilities/ script). The
// target's existence on the host is not required at grant time — a grant for a
// not-yet-present target is recorded and simply fails closed at execution until
// the target exists (the operator may grant ahead of authoring the Shortcut).
func (s *Server) handleGrantCapability(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req CapabilityGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := project.ValidateCapabilityTarget(req.Target); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	grant, err := p.AddCapabilityGrant(project.CapabilityGrant{Target: req.Target, GrantedAt: time.Now().UTC()})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "capability.grant", Island: name, Scope: grant.Target, Decision: "allowed",
	})
	writeJSON(w, http.StatusCreated, capabilityView(grant))
}

// handleRevokeCapability drops a capability grant by its target name.
func (s *Server) handleRevokeCapability(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	target := r.PathValue("target")
	removed, ok := p.RemoveCapabilityGrant(target)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no capability grant %q", name, target))
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "capability.revoke", Island: name, Scope: removed.Target, Decision: "allowed",
	})
	w.WriteHeader(http.StatusNoContent)
}

func capabilityView(c project.CapabilityGrant) CapabilityGrantView {
	return CapabilityGrantView{Target: c.Target, GrantedAt: c.GrantedAt}
}

func capabilityViews(cs []project.CapabilityGrant) []CapabilityGrantView {
	out := make([]CapabilityGrantView, 0, len(cs))
	for _, c := range cs {
		out = append(out, capabilityView(c))
	}
	return out
}
