package api

import (
	"context"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/spawn"
)

// DefaultSpawnReapInterval is how often the reaper sweeps for ephemeral
// sub-agents to clean up. Minute granularity is plenty (TTLs are coarse; the
// exit/parent cases also resolve on the next sweep).
const DefaultSpawnReapInterval = time.Minute

// DefaultEphemeralTTL is a leak backstop: the maximum lifetime applied to an
// ephemeral sub-agent when its grant sets no TTL (--ttl unset). Without it, a
// granted sub-agent whose parent stays alive and whose session never reports
// "exited" (e.g. a lingering tmux shell, or an agent spawned over the API that
// never started a real process — the d7 case in a3 #62) would live forever,
// silently holding a max_concurrent slot. An ephemeral is by definition
// short-lived; an operator who wants longer sets an explicit grant --ttl. This
// is a ceiling, not a typical lifetime — the parent-removed and exited triggers
// reap most sub-agents far sooner.
const DefaultEphemeralTTL = time.Hour

// shouldReap decides whether an ephemeral sub-agent is due for cleanup, and why.
// Pure (no I/O) so the policy is unit-tested directly. Triggers: parent gone
// (its spawner left the roster), TTL elapsed since creation, or the agent exited
// (its session died — state "exited"/"stopped"). Non-ephemeral agents are never
// reaped by this path. The ttl passed in is the grant's TTL or, when the grant
// sets none, DefaultEphemeralTTL (resolved by spawnTTL) — so a granted ephemeral
// always has a finite lifetime and can't leak (a3 #62: the no-TTL d7 case).
func shouldReap(a project.AgentSpec, parentPresent bool, ttl time.Duration, state string, now time.Time) (bool, string) {
	if !a.Ephemeral {
		return false, ""
	}
	if a.SpawnedBy != "" && !parentPresent {
		return true, "parent removed"
	}
	if ttl > 0 && !a.CreatedAt.IsZero() && now.Sub(a.CreatedAt) > ttl {
		return true, "ttl expired"
	}
	if state == "exited" || state == "stopped" {
		return true, "exited"
	}
	return false, ""
}

// RunSpawnReaper periodically reaps ephemeral sub-agents that have exited, aged
// out (grant TTL), or whose parent is gone — freeing their max_concurrent slot.
// No-op islands (no ephemeral agents) cost a cheap roster check. Mirrors the
// idle-hibernate loop.
func (s *Server) RunSpawnReaper(ctx context.Context) {
	t := time.NewTicker(DefaultSpawnReapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanSpawnReap(ctx)
		}
	}
}

// scanSpawnReap is one reaper sweep across all islands.
func (s *Server) scanSpawnReap(ctx context.Context) {
	projects, err := project.List()
	if err != nil {
		return
	}
	now := time.Now()
	for _, p := range projects {
		p.EnsureAgents()
		if !hasEphemeral(p.Agents) {
			continue
		}
		// Liveness execs into the container, so snapshot it OUTSIDE the per-island
		// lock (don't hold the lock across a slow/wedged exec). TTL + parent are
		// decided from the roster under the lock; a just-revived agent reaped on a
		// stale liveness snapshot is acceptable (ephemeral agents don't restart).
		live := s.agentsLive(ctx, p)
		state := map[string]string{}
		if live {
			for i := range p.Agents {
				a := &p.Agents[i]
				if a.Ephemeral && a.Tmux != "" {
					state[a.ID] = s.agentLiveness(ctx, p, a)
				}
			}
		}
		ttl := s.spawnTTL(p.Name)
		s.reapIsland(ctx, p.Name, ttl, state, now)
	}
}

// reapIsland reaps the due ephemeral agents of one island atomically under its
// projectLock (reload → decide → remove → save), then cleans up sessions detached.
func (s *Server) reapIsland(ctx context.Context, island string, ttl time.Duration, state map[string]string, now time.Time) {
	lock := s.projectLock(island)
	lock.Lock()
	p, err := project.Load(island)
	if err != nil {
		lock.Unlock()
		return
	}
	p.EnsureAgents()
	present := map[string]bool{}
	for _, a := range p.Agents {
		present[a.ID] = true
	}
	var reaped []project.AgentSpec
	for _, a := range p.Agents {
		if reap, _ := shouldReap(a, present[a.SpawnedBy], ttl, state[a.ID], now); reap {
			reaped = append(reaped, a)
		}
	}
	for _, a := range reaped {
		p.RemoveAgent(a.ID)
		s.clearAgentError(island, a.ID)
	}
	if len(reaped) > 0 {
		_ = p.Save()
	}
	lock.Unlock()
	s.finishReap(ctx, p, island, reaped)
}

// finishReap does the off-lock cleanup for reaped agents: kill their session +
// worktree (best-effort, detached), emit the removal event, and ledger spawn.reap.
func (s *Server) finishReap(ctx context.Context, p *project.Project, island string, reaped []project.AgentSpec) {
	for _, a := range reaped {
		ac := a
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.removeAgentSession(c, p, &ac)
		}()
		s.emit(events.Event{Type: events.TypeIslandAgentRemoved, Island: island, Agent: ac.ID, Payload: map[string]any{"reason": "reaped"}})
		s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{Type: "spawn.reap", Island: island, Detail: "reaped sub-agent " + ac.ID, Decision: "allowed"})
	}
	_ = ctx
}

// reapAllEphemeral removes every ephemeral sub-agent of an island regardless of
// state — used when the operator REVOKES the spawn grant (the agents lose their
// authorization, so they're cleaned up immediately).
func (s *Server) reapAllEphemeral(ctx context.Context, island string) {
	lock := s.projectLock(island)
	lock.Lock()
	p, err := project.Load(island)
	if err != nil {
		lock.Unlock()
		return
	}
	p.EnsureAgents()
	var reaped []project.AgentSpec
	for _, a := range p.Agents {
		if a.Ephemeral {
			reaped = append(reaped, a)
		}
	}
	for _, a := range reaped {
		p.RemoveAgent(a.ID)
		s.clearAgentError(island, a.ID)
	}
	if len(reaped) > 0 {
		_ = p.Save()
	}
	lock.Unlock()
	s.finishReap(ctx, p, island, reaped)
}

// spawnTTL is the per-sub-agent TTL from the island's grant (0 if no grant/TTL).
func (s *Server) spawnTTL(island string) time.Duration {
	st, err := spawn.Load()
	if err != nil {
		return 0
	}
	if g, ok := st.Get(island); ok {
		if g.TTL > 0 {
			return g.TTL
		}
		// Granted but no explicit --ttl: fall back to the leak backstop so an
		// ephemeral can't live forever (see DefaultEphemeralTTL).
		return DefaultEphemeralTTL
	}
	return 0
}

func hasEphemeral(agents []project.AgentSpec) bool {
	for _, a := range agents {
		if a.Ephemeral {
			return true
		}
	}
	return false
}
