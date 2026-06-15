package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aoos/dejima/internal/events"
)

// SubscribeWebhookRequest is the body of POST /v1/events/subscribe.
type SubscribeWebhookRequest struct {
	URL    string        `json:"url"`
	Secret string        `json:"secret,omitempty"`
	Events []events.Type `json:"events,omitempty"`
}

// AgentEventRequest is the body of POST /v1/internal/agent-event.
type AgentEventRequest struct {
	Island  string         `json:"island"`
	Agent   string         `json:"agent,omitempty"`
	Type    events.Type    `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

func (s *Server) listSubscriptions(w http.ResponseWriter, _ *http.Request) {
	if s.events == nil {
		writeJSON(w, http.StatusOK, []events.Subscription{})
		return
	}
	writeJSON(w, http.StatusOK, s.events.List())
}

func (s *Server) subscribeWebhook(w http.ResponseWriter, r *http.Request) {
	if s.events == nil {
		writeError(w, http.StatusInternalServerError, errEventsDisabled)
		return
	}
	var req SubscribeWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Reject unknown event types up front: a typo'd filter would otherwise create
	// a subscription that silently matches nothing.
	for _, t := range req.Events {
		if !events.KnownType(t) {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf("unknown event type %q (see `dejima webhook events`)", t))
			return
		}
	}
	sub, err := s.events.Subscribe(events.Subscription{
		URL:    req.URL,
		Secret: req.Secret,
		Events: req.Events,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (s *Server) unsubscribeWebhook(w http.ResponseWriter, r *http.Request) {
	if s.events == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.events.Unsubscribe(r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentEvent(w http.ResponseWriter, r *http.Request) {
	var req AgentEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, errMissingType)
		return
	}
	// A token-authenticated caller (the in-island notify hooks) may only emit for
	// its own island: override whatever the body claims with the token's island,
	// so one island can't spoof another's telemetry. Trusted callers (operator
	// socket / tailnet) carry no token island and keep the body's value.
	if island := TokenIslandFromContext(r.Context()); island != "" {
		req.Island = island
	}
	s.emit(events.Event{
		Type:    req.Type,
		Island:  req.Island,
		Agent:   req.Agent,
		Payload: req.Payload,
	})
	w.WriteHeader(http.StatusAccepted)
}

// sentinel errors kept terse — callers convert to HTTP envelopes.
var (
	errEventsDisabled = staticErr("event manager not configured")
	errMissingType    = staticErr("type is required")
)

type staticErr string

func (e staticErr) Error() string { return string(e) }
