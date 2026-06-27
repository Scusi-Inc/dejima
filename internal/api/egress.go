package api

import (
	"net/http"

	"github.com/aoos/dejima/internal/egress"
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
