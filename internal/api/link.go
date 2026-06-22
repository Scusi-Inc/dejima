package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/project"
)

// LinkGrantRequest is the body of POST /v1/links: an operator authorizes a
// directional info channel from→to on a topic. Operator-only. The grant is
// island→island; the recipient agent is chosen per message, not granted.
type LinkGrantRequest struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Topic string `json:"topic"`
}

// LinksResponse is the list of grants (GET /v1/links).
type LinksResponse struct {
	Grants []link.Grant `json:"grants"`
}

// LinkSendRequest is the body of POST /v1/islands/{name}/link/send: island
// {name} (the sender, pinned by its token) sends an info message addressed to a
// specific agent (ToAgent) in island To, on Topic. Allowed only if an operator
// granted {name}→To on Topic. FromAgent is the sending agent's id (self-reported
// within the sender's own trust domain; the island half of provenance is
// daemon-stamped, not this).
type LinkSendRequest struct {
	To        string `json:"to"`         // destination island
	ToAgent   string `json:"to_agent"`   // destination agent (required — no island-wide broadcast)
	Topic     string `json:"topic"`      // the granted topic
	Payload   string `json:"payload"`    //
	FromAgent string `json:"from_agent"` // sending agent id (optional)
}

// grantLink authorizes a directional info channel (operator-only — absent from
// tokenRouteAccess, so island tokens can't open their own channel).
func (s *Server) grantLink(w http.ResponseWriter, r *http.Request) {
	var req LinkGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	from, to, topic := strings.TrimSpace(req.From), strings.TrimSpace(req.To), strings.TrimSpace(req.Topic)
	if from == "" || to == "" || topic == "" {
		writeError(w, http.StatusBadRequest, errors.New("from, to, and topic are all required"))
		return
	}
	if from == to {
		writeError(w, http.StatusBadRequest, errors.New("from and to must be different islands"))
		return
	}
	if _, err := project.Load(from); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such island %q (from)", from))
		return
	}
	if _, err := project.Load(to); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such island %q (to)", to))
		return
	}
	var granted link.Grant
	if _, err := link.Update(func(st *link.Store) error {
		granted = st.Grant(link.Grant{From: from, To: to, Topic: topic})
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "link.grant", Island: from, Scope: topic, Detail: "→ " + to, Decision: "allowed",
	})
	s.log.Info("link granted", "from", from, "to", to, "topic", topic)
	writeJSON(w, http.StatusCreated, granted)
}

// revokeLink drops a grant (operator-only). from/to/topic are query params.
func (s *Server) revokeLink(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	topic := r.URL.Query().Get("topic")
	if from == "" || to == "" || topic == "" {
		writeError(w, http.StatusBadRequest, errors.New("from, to, and topic query params are required"))
		return
	}
	var removed bool
	if _, err := link.Update(func(st *link.Store) error {
		removed = st.Revoke(from, to, topic)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such link grant %s→%s/%s", from, to, topic))
		return
	}
	s.ledgerAppend(ledger.Entry{Type: "link.revoke", Island: from, Scope: topic, Detail: "→ " + to})
	w.WriteHeader(http.StatusNoContent)
}

// listLinks returns every grant (operator-only).
func (s *Server) listLinks(w http.ResponseWriter, _ *http.Request) {
	st, err := link.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, LinksResponse{Grants: st.List()})
}

// sendLink delivers an info message from island {name} to a specific agent in
// another island over a granted channel — into that agent's ORDINARY mailbox
// (the single durable inbox + audit point), stamped with daemon-controlled
// cross-island provenance. {name} is the SENDER, pinned to the caller's token by
// accessOwnIsland — an island can only send as itself. Default-deny: no matching
// grant ⇒ refused + ledgered link.deny; the agent can never self-approve or reach
// an ungranted island.
func (s *Server) sendLink(w http.ResponseWriter, r *http.Request) {
	from := r.PathValue("name")
	if _, err := project.Load(from); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req LinkSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	to, toAgent, topic := strings.TrimSpace(req.To), strings.TrimSpace(req.ToAgent), strings.TrimSpace(req.Topic)
	if to == "" || toAgent == "" || topic == "" || req.Payload == "" {
		writeError(w, http.StatusBadRequest, errors.New("to, to_agent, topic, and payload are required"))
		return
	}
	// The gate: deny-all unless the operator granted from→to on this topic.
	st, err := link.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, ok := st.Allowed(from, to, topic); !ok {
		s.ledgerAppend(ledger.Entry{
			Type: "link.deny", Island: from, Scope: topic, Detail: "→ " + to + "/" + toAgent, Decision: "denied",
		})
		writeError(w, http.StatusForbidden,
			fmt.Errorf("no link grant %s→%s on topic %q (cross-island is deny-all; operator grants with `dejima link grant`)", from, to, topic))
		return
	}
	// The target agent must exist in the destination island — a cross-island
	// message must never silently vanish into a non-existent recipient.
	pTo, err := project.Load(to)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("destination island %q not found", to))
		return
	}
	pTo.EnsureAgents()
	if _, ok := pTo.AgentByID(toAgent); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", to, toAgent))
		return
	}
	// Deliver into the recipient agent's ordinary mailbox with daemon-stamped
	// provenance (Origin.SourceIsland = the token-pinned sender island).
	msg := s.mailbox.DeliverExternal(to, from, strings.TrimSpace(req.FromAgent), toAgent, topic, req.Payload)
	s.ledgerAppend(ledger.Entry{
		Type: "link.message", Island: from, Scope: topic, Detail: "→ " + to + "/" + toAgent, Decision: "allowed",
	})
	writeJSON(w, http.StatusCreated, msg)
}
