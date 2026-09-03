package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/policy"
	"github.com/aoos/dejima/internal/project"
)

// PolicyAddRequest creates a scoped auto-approve rule: for link From→To,
// auto-approve action-type Action up to MaxCount times (<=0 = unlimited within
// the window) until TTL elapses. TTL is a Go duration string ("1h", "30m"); ""
// means no expiry (discouraged). Destructive actions are never auto-approved
// regardless of any rule — that's enforced at request time, not here.
type PolicyAddRequest struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Action   string `json:"action"`
	MaxCount int    `json:"max_count,omitempty"`
	TTL      string `json:"ttl,omitempty"`
}

// PolicyListResponse is the operator view of active auto-approve rules.
type PolicyListResponse struct {
	Rules []policy.Rule `json:"rules"`
}

// listPolicy returns the active auto-approve rules (operator).
func (s *Server) listPolicy(w http.ResponseWriter, _ *http.Request) {
	st, err := policy.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, PolicyListResponse{Rules: st.List()})
}

// addPolicy creates or replaces an auto-approve rule (operator). Adding a rule is
// a privileged, ledgered act: a blanket auto-approve is a bypass vector.
func (s *Server) addPolicy(w http.ResponseWriter, r *http.Request) {
	var req PolicyAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	from, to, action := strings.TrimSpace(req.From), strings.TrimSpace(req.To), strings.TrimSpace(req.Action)
	if from == "" || to == "" || action == "" {
		writeError(w, http.StatusBadRequest, errors.New("from, to, and action are all required"))
		return
	}
	if from == to {
		writeError(w, http.StatusBadRequest, errors.New("from and to must be different islands"))
		return
	}
	if req.MaxCount < 0 {
		writeError(w, http.StatusBadRequest, errors.New("max_count must be >= 0 (0 = unlimited within the ttl)"))
		return
	}
	var ttl time.Duration
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid ttl %q: %w", req.TTL, err))
			return
		}
		if d < 0 {
			writeError(w, http.StatusBadRequest, errors.New("ttl must not be negative"))
			return
		}
		ttl = d
	}
	if _, err := project.Load(from); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such island %q (from)", from))
		return
	}
	if _, err := project.Load(to); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such island %q (to)", to))
		return
	}
	by := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		by = id.Subject
	}
	var added policy.Rule
	if _, err := policy.Update(func(st *policy.Store) error {
		added = st.Add(policy.Rule{From: from, To: to, Action: action, MaxCount: req.MaxCount, CreatedBy: by}, ttl)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "policy.add", Island: from, Scope: action,
		Detail: fmt.Sprintf("→ %s auto-approve [%s] max=%d ttl=%s", to, action, req.MaxCount, req.TTL),
		Actor:  by, Decision: "allowed",
	})
	s.log.Info("policy rule added", "from", from, "to", to, "action", action, "max", req.MaxCount, "ttl", req.TTL, "by", by)
	writeJSON(w, http.StatusCreated, added)
}

// removePolicy deletes an auto-approve rule (operator); ledgered.
func (s *Server) removePolicy(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if from == "" || to == "" || action == "" {
		writeError(w, http.StatusBadRequest, errors.New("from, to, and action query params are required"))
		return
	}
	var removed bool
	if _, err := policy.Update(func(st *policy.Store) error {
		removed = st.Remove(from, to, action)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such auto-approve rule %s→%s [%s]", from, to, action))
		return
	}
	by := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		by = id.Subject
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "policy.remove", Island: from, Scope: action, Detail: "→ " + to + " [" + action + "]", Actor: by,
	})
	w.WriteHeader(http.StatusNoContent)
}
