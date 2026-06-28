package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/spawn"
)

// SpawnGrantRequest is the body of POST /v1/islands/{name}/spawn-grant — the
// operator opting an island into agent-initiated ephemeral sub-agents, with an
// explicit budget. Operator-only (see roleauth); an in-island token can never
// reach this route (it isn't in tokenRouteAccess), only spawn within a grant.
type SpawnGrantRequest struct {
	MaxConcurrent  int      `json:"max_concurrent"`
	MaxTotal       int      `json:"max_total,omitempty"`
	Types          []string `json:"types,omitempty"`
	TTL            string   `json:"ttl,omitempty"` // per-sub-agent lifetime, e.g. "1h"
	PerAgentMemory string   `json:"per_agent_memory,omitempty"`
	PerAgentCPUs   string   `json:"per_agent_cpus,omitempty"`
}

// SpawnGrantResponse echoes an island's current grant. Granted=false means no
// grant (the deny default — the island's agents cannot spawn).
type SpawnGrantResponse struct {
	Granted bool         `json:"granted"`
	Grant   *spawn.Grant `json:"grant,omitempty"`
}

func (s *Server) getSpawnGrant(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	st, err := spawn.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	g, ok := st.Get(name)
	if !ok {
		writeJSON(w, http.StatusOK, SpawnGrantResponse{Granted: false})
		return
	}
	writeJSON(w, http.StatusOK, SpawnGrantResponse{Granted: true, Grant: &g})
}

func (s *Server) setSpawnGrant(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such island %q", name))
		return
	}
	var req SpawnGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var ttl time.Duration
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid ttl %q: %w", req.TTL, err))
			return
		}
		ttl = d
	}
	by := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		by = id.Subject
	}
	g := spawn.Grant{
		Island: name, MaxConcurrent: req.MaxConcurrent, MaxTotal: req.MaxTotal,
		Types: req.Types, TTL: ttl, PerAgentMemory: req.PerAgentMemory,
		PerAgentCPUs: req.PerAgentCPUs, CreatedBy: by,
	}
	if _, err := spawn.Update(func(st *spawn.Store) error { return st.Set(g) }); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type:   "spawn.grant",
		Island: name,
		Detail: fmt.Sprintf("max_concurrent=%d max_total=%d types=%v ttl=%s mem=%s cpus=%s",
			g.MaxConcurrent, g.MaxTotal, g.Types, g.TTL, g.PerAgentMemory, g.PerAgentCPUs),
		Actor: by, Decision: "allowed",
	})
	writeJSON(w, http.StatusOK, SpawnGrantResponse{Granted: true, Grant: &g})
}

func (s *Server) revokeSpawnGrant(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	removed := false
	if _, err := spawn.Update(func(st *spawn.Store) error {
		removed = st.Remove(name)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, errors.New("no spawn grant for this island"))
		return
	}
	by := "operator"
	if id, ok := IdentityFromContext(r.Context()); ok && id.Subject != "" {
		by = id.Subject
	}
	s.ledgerAppend(ledger.Entry{Type: "spawn.revoke", Island: name, Actor: by, Decision: "allowed"})
	w.WriteHeader(http.StatusNoContent)
}
