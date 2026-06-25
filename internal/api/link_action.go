package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/project"
)

// LinkActionRequest is the body of POST /v1/islands/{name}/link/action: island
// {name} (sender, token-pinned) asks {to}'s agent {to_agent} to run the NAMED,
// typed {action} (with {params}) over the granted channel {topic}. There is no
// free-form path — only named actions the destination exposed.
type LinkActionRequest struct {
	To        string `json:"to"`
	ToAgent   string `json:"to_agent"`
	Topic     string `json:"topic"`
	Action    string `json:"action"`
	Params    string `json:"params,omitempty"`
	FromAgent string `json:"from_agent,omitempty"`
}

// LinkActionResponse reports the outcome of an action request: "executed" (a
// pre-authorized action delivered immediately) or "pending" (queued for operator
// approval, with the pending id).
type LinkActionResponse struct {
	Status  string `json:"status"` // "executed" | "pending"
	Pending string `json:"pending,omitempty"`
}

// LinkExposedResponse is an island's exposed action types.
type LinkExposedResponse struct {
	Island  string   `json:"island"`
	Actions []string `json:"actions"`
}

// LinkPendingResponse is the queued action approvals (operator view).
type LinkPendingResponse struct {
	Pending []link.ActionRequest `json:"pending"`
}

// exposeAction adds a named action type to an island's exposed set (operator).
// Cross-island delegation is deny-all: another island can invoke only actions
// this island has exposed AND a grant authorizes.
func (s *Server) exposeAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), strings.TrimSpace(r.PathValue("action"))
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if action == "" {
		writeError(w, http.StatusBadRequest, errors.New("action is required"))
		return
	}
	p.AddLinkAction(action)
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{Type: "link.expose", Island: name, Scope: action, Decision: "allowed"})
	writeJSON(w, http.StatusOK, LinkExposedResponse{Island: name, Actions: p.LinkActions})
}

// unexposeAction removes an exposed action type (operator).
func (s *Server) unexposeAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), strings.TrimSpace(r.PathValue("action"))
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !p.RemoveLinkAction(action) {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q does not expose action %q", name, action))
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{Type: "link.unexpose", Island: name, Scope: action})
	w.WriteHeader(http.StatusNoContent)
}

// listExposedActions returns an island's exposed action types (operator).
func (s *Server) listExposedActions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, LinkExposedResponse{Island: name, Actions: p.LinkActions})
}

// requestAction is the action gate (token-scoped; the sender is pinned to its
// token). Deny-all + fail-closed: it needs a link grant for the channel AND the
// destination to expose the action. A pre-authorized action (in the grant's
// Actions allowlist) executes immediately; anything else is queued for operator
// approval and a link.action-pending event fires. A free-form prompt is never an
// action — only named, exposed action types.
func (s *Server) requestAction(w http.ResponseWriter, r *http.Request) {
	from := r.PathValue("name")
	if _, err := project.Load(from); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req LinkActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	to, toAgent, topic, action := strings.TrimSpace(req.To), strings.TrimSpace(req.ToAgent), strings.TrimSpace(req.Topic), strings.TrimSpace(req.Action)
	if to == "" || toAgent == "" || topic == "" || action == "" {
		writeError(w, http.StatusBadRequest, errors.New("to, to_agent, topic, and action are required"))
		return
	}
	ar := link.ActionRequest{From: from, FromAgent: strings.TrimSpace(req.FromAgent), To: to, ToAgent: toAgent, Topic: topic, Action: action, Tier: link.ClassifyTier(action), Params: req.Params}
	grant, allowed, exposed, agentOK, err := s.linkActionGate(ar)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !allowed {
		s.ledgerLinkDeny(ar, "no channel grant")
		writeError(w, http.StatusForbidden,
			fmt.Errorf("no link grant %s→%s on topic %q (cross-island is deny-all)", from, to, topic))
		return
	}
	if !agentOK {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", to, toAgent))
		return
	}
	if !exposed {
		s.ledgerLinkDeny(ar, "action not exposed by destination")
		writeError(w, http.StatusForbidden,
			fmt.Errorf("island %q does not expose action %q", to, action))
		return
	}
	// Pre-authorized in the grant's allowlist → execute immediately.
	if actionInList(action, grant.Actions) {
		s.executeLinkAction(ar, "")
		writeJSON(w, http.StatusCreated, LinkActionResponse{Status: "executed"})
		return
	}
	// Otherwise queue it for operator approval and notify.
	pending := s.linkQueue.Add(ar)
	s.emit(events.Event{
		Type:   events.TypeLinkActionPending,
		Island: from,
		Agent:  ar.FromAgent,
		Payload: map[string]any{
			"id": pending.ID, "to": to, "to_agent": toAgent, "topic": topic,
			"action": action, "tier": string(pending.Tier),
		},
	})
	s.ledgerAppend(ledger.Entry{
		Type: "link.action", Island: from, Scope: topic, Detail: "→ " + to + "/" + toAgent + " [" + action + "] pending", Decision: "pending",
	})
	writeJSON(w, http.StatusAccepted, LinkActionResponse{Status: "pending", Pending: pending.ID})
}

// linkActionGate resolves the three gate facts for an action request: the
// matching grant (+ whether it exists), whether the destination exposes the
// action, and whether the target agent exists.
func (s *Server) linkActionGate(ar link.ActionRequest) (grant link.Grant, allowed, exposed, agentOK bool, err error) {
	st, err := link.Load()
	if err != nil {
		return link.Grant{}, false, false, false, err
	}
	grant, allowed = st.Allowed(ar.From, ar.To, ar.Topic)
	pTo, perr := project.Load(ar.To)
	if perr != nil {
		return grant, allowed, false, false, nil // dest gone → not exposed, no agent
	}
	pTo.EnsureAgents()
	_, agentOK = pTo.AgentByID(ar.ToAgent)
	exposed = pTo.ExposesAction(ar.Action)
	return grant, allowed, exposed, agentOK, nil
}

// executeLinkAction delivers the named action into the destination agent's
// mailbox (typed Action, cross-island Origin) and ledgers link.action. When
// approvedBy is non-empty it also writes the approval record (who, when).
func (s *Server) executeLinkAction(ar link.ActionRequest, approvedBy string) {
	if approvedBy != "" {
		s.ledgerAppend(ledger.Entry{
			Type: "link.approve", Island: ar.From, Scope: ar.Topic,
			Detail: "→ " + ar.To + "/" + ar.ToAgent + " [" + ar.Action + "]", Actor: approvedBy, Decision: "allowed",
		})
	}
	s.mailbox.DeliverAction(ar.To, ar.From, ar.FromAgent, ar.ToAgent, ar.Topic, ar.Action, ar.Params)
	s.ledgerAppend(ledger.Entry{
		Type: "link.action", Island: ar.From, Scope: ar.Topic,
		Detail: "→ " + ar.To + "/" + ar.ToAgent + " [" + ar.Action + "]", Decision: "allowed",
	})
}

func (s *Server) ledgerLinkDeny(ar link.ActionRequest, reason string) {
	s.ledgerAppend(ledger.Entry{
		Type: "link.deny", Island: ar.From, Scope: ar.Topic,
		Detail: "→ " + ar.To + "/" + ar.ToAgent + " [" + ar.Action + "] " + reason, Decision: "denied",
	})
}

// listPendingActions returns the queued action approvals (operator).
func (s *Server) listPendingActions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, LinkPendingResponse{Pending: s.linkQueue.List()})
}

// approveAction approves a pending action and executes it (operator role only —
// agents can never self-approve). Re-checks the gate at approval time so a
// revoked grant or unexposed action can't be executed from a stale pending.
func (s *Server) approveAction(w http.ResponseWriter, r *http.Request) {
	ar, ok := s.linkQueue.Take(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no such pending action (unknown or expired — fail-closed)"))
		return
	}
	grant, allowed, exposed, agentOK, err := s.linkActionGate(ar)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !allowed || !exposed || !agentOK {
		s.ledgerLinkDeny(ar, "gate no longer satisfied at approval")
		writeError(w, http.StatusConflict, errors.New("the channel grant or exposed action changed; refusing to execute"))
		return
	}
	_ = grant
	approver := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		approver = id.Subject
	}
	s.executeLinkAction(ar, approver)
	writeJSON(w, http.StatusOK, LinkActionResponse{Status: "executed", Pending: ar.ID})
}

// denyAction denies a pending action (operator role only); fail-closed, ledgered.
func (s *Server) denyAction(w http.ResponseWriter, r *http.Request) {
	ar, ok := s.linkQueue.Take(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no such pending action (unknown or expired)"))
		return
	}
	denier := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		denier = id.Subject
	}
	s.ledgerAppend(ledger.Entry{
		Type: "link.deny", Island: ar.From, Scope: ar.Topic,
		Detail: "→ " + ar.To + "/" + ar.ToAgent + " [" + ar.Action + "] operator-denied", Actor: denier, Decision: "denied",
	})
	w.WriteHeader(http.StatusNoContent)
}

func actionInList(action string, list []string) bool {
	for _, a := range list {
		if a == action {
			return true
		}
	}
	return false
}
