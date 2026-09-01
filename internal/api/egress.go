package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aoos/dejima/internal/egress"
	"github.com/aoos/dejima/internal/ledger"
)

// EgressEventsResponse is the read-API shape for an island's recent outbound
// connections, as observed by the egress proxy (Phase 1). Events is empty when
// the proxy is disabled or the island hasn't connected out yet.
type EgressEventsResponse struct {
	Events []egress.Event `json:"events"`
}

// handleIslandEgress serves an island's recent egress observations. It reads the
// in-memory log only — no engine call — so it's cheap and safe to poll. When the
// egress proxy isn't enabled the log is nil and the list is empty (not an
// error): clients render "egress observability not enabled / nothing yet".
func (s *Server) handleIslandEgress(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var events []egress.Event
	if s.egressLog != nil {
		events = s.egressLog.List(name)
	}
	if events == nil {
		events = []egress.Event{}
	}
	writeJSON(w, http.StatusOK, EgressEventsResponse{Events: events})
}

// handleGetEgressPolicy returns an island's egress policy (Phase 2). When the
// proxy is disabled or no policy is set, it reports the effective default:
// observe (allow-all, log-only).
func (s *Server) handleGetEgressPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var p egress.IslandPolicy
	if s.egressPolicy != nil {
		p = s.egressPolicy.Get(name)
	}
	if p.Mode == "" {
		p.Mode = egress.ModeObserve // surface the effective posture, not ""
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePatchEgressPolicy applies an incremental change to an island's egress
// policy (set mode, add/remove allow/deny hosts) and ledgers it. Operator-only
// (see roleauth). Returns the resulting policy.
func (s *Server) handlePatchEgressPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.egressPolicy == nil {
		writeError(w, http.StatusServiceUnavailable,
			errors.New("egress proxy not enabled on this daemon (start it with --egress-proxy)"))
		return
	}
	var patch egress.PolicyPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.egressPolicy.Apply(name, patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	by := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		by = id.Subject
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type:     "egress.policy",
		Island:   name,
		Detail:   fmt.Sprintf("mode=%s allow=%v deny=%v", result.Mode, result.Allow, result.Deny),
		Actor:    by,
		Decision: "allowed",
	})
	writeJSON(w, http.StatusOK, result)
}
