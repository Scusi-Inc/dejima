package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/spawn"
)

// spawnDenied is a refused agent-initiated spawn; addAgent maps it to 403 + a
// spawn.deny ledger entry.
type spawnDenied struct{ reason string }

func (e *spawnDenied) Error() string { return e.reason }

// authorizeSpawn enforces the spawn gate for an agent-INITIATED add (an in-island
// token caller). It MUST be called under the island's projectLock so the budget
// check is ATOMIC with the create that follows — a3's load-bearing requirement:
// a burst of concurrent spawns can't TOCTOU past max_concurrent/max_total. p is
// the freshly-loaded roster (so the live count reflects already-saved spawns) and
// spec is the resolved agent spec. Returns nil to allow, or *spawnDenied to refuse.
//
// The budget is the hard, daemon-enforced safety (never client-trusted). The
// depth-cap (no recursion) is structural defense-in-depth via the self-reported
// lineage — sound here because co-located agents are ONE trust domain (shared
// island token/sandbox); cryptographic token-separation belongs to the future
// ephemeral-island variant.
func (s *Server) authorizeSpawn(p *project.Project, spec project.AgentSpec) error {
	st, err := spawn.Load()
	if err != nil {
		return err
	}
	g, ok := st.Get(p.Name)
	if !ok {
		return &spawnDenied{"no spawn grant — this island's agents cannot spawn sub-agents (operator: `dejima spawn grant`)"}
	}
	if !spec.Ephemeral {
		return &spawnDenied{"an in-island token may only create EPHEMERAL sub-agents (set ephemeral=true)"}
	}
	if len(g.Types) > 0 && !containsStr(g.Types, spec.Type) {
		return &spawnDenied{fmt.Sprintf("agent type %q is not allowed by the grant (allowed: %v)", spec.Type, g.Types)}
	}
	// Depth cap 1: a sub-agent (itself ephemeral) cannot spawn.
	p.EnsureAgents()
	if parent := strings.TrimSpace(spec.SpawnedBy); parent != "" {
		if a, ok := p.AgentByID(parent); ok && a.Ephemeral {
			return &spawnDenied{"a spawned sub-agent cannot itself spawn (depth capped at 1)"}
		}
	}
	// Budget — counted from the locked roster (atomic with the create).
	live := 0
	for _, a := range p.Agents {
		if a.Ephemeral {
			live++
		}
	}
	if live >= g.MaxConcurrent {
		return &spawnDenied{fmt.Sprintf("spawn budget reached: %d/%d concurrent sub-agents", live, g.MaxConcurrent)}
	}
	if g.MaxTotal > 0 && g.Used >= g.MaxTotal {
		return &spawnDenied{fmt.Sprintf("spawn budget reached: %d/%d lifetime spawns", g.Used, g.MaxTotal)}
	}
	return nil
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

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
