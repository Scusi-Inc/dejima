package api

import (
	"context"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// HeartbeatMonitorInterval is how often the daemon checks for silent agents.
const HeartbeatMonitorInterval = time.Minute

// heartbeatTransition is one edge in an island's agent liveness: it just went
// silent (Silent=true) or its heartbeat just came back (Silent=false).
type heartbeatTransition struct {
	island, agent string
	silent        bool
}

// RunHeartbeatMonitor is the "monitor the monitor" alerter: it watches running
// islands and raises an operator alert (agent.silent, ledgered) when an agent's
// agent-state heartbeat goes silent past the grace — a stalled/crashed agent, or
// a skewed in-island shim that can't POST — and a recovery (agent.recovered) when
// it returns. Edge-triggered, so an ongoing silence alerts ONCE, not every tick.
// Runs unconditionally in its own goroutine; returns when ctx is cancelled.
func (s *Server) RunHeartbeatMonitor(ctx context.Context) {
	t := time.NewTicker(HeartbeatMonitorInterval)
	defer t.Stop()
	s.log.Info("heartbeat monitor enabled", "tick", HeartbeatMonitorInterval.String(), "grace", heartbeatGrace.String())
	silent := map[string]bool{} // island → currently-alerted-silent
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, tr := range s.scanHeartbeats(ctx, silent, time.Now()) {
				s.emitHeartbeatAlert(tr)
			}
		}
	}
}

// scanHeartbeats compares each island's current silence against the caller-owned
// `silent` set and returns the edges (newly-silent / recovered), updating the set.
// Islands that vanished are forgotten so the map can't grow unbounded. Pure of
// I/O beyond reading project + runtime state, so the edge logic is unit-testable.
func (s *Server) scanHeartbeats(ctx context.Context, silent map[string]bool, now time.Time) []heartbeatTransition {
	projects, err := project.List()
	if err != nil {
		return nil
	}
	var out []heartbeatTransition
	seen := map[string]bool{}
	for _, p := range projects {
		seen[p.Name] = true
		running := false
		if status, err := s.rt.Status(ctx, p.ContainerName()); err == nil {
			running = status == runtime.StatusRunning
		}
		agentID, st := s.primaryHeartbeat(p)
		isSilent := heartbeatSilent(running, st, p.LastUsedAt, now)
		switch {
		case isSilent && !silent[p.Name]:
			silent[p.Name] = true
			out = append(out, heartbeatTransition{island: p.Name, agent: agentID, silent: true})
		case !isSilent && silent[p.Name]:
			delete(silent, p.Name)
			out = append(out, heartbeatTransition{island: p.Name, agent: agentID, silent: false})
		}
	}
	for name := range silent {
		if !seen[name] {
			delete(silent, name) // island gone — drop its alert state
		}
	}
	return out
}

// primaryHeartbeat returns the island's primary agent id and its latest
// agent-state (nil if none yet).
func (s *Server) primaryHeartbeat(p *project.Project) (string, *AgentStateInfo) {
	id := ""
	if a := p.PrimaryAgent(); a != nil {
		id = a.ID
	}
	return id, s.agentStateOf(p.Name, id)
}

// heartbeatSilent decides whether a running island's agent is silent: either it
// has emitted NO heartbeat and its last boot is older than heartbeatGrace (the
// same zero-heartbeat rule as never_heard_from), or its last heartbeat is older
// than heartbeatGrace (it went silent). A hibernated/stopped island is never
// silent — that's expected, not an alert. Pure, so it's unit-tested directly.
func heartbeatSilent(running bool, st *AgentStateInfo, sinceTime, now time.Time) bool {
	if !running {
		return false
	}
	if st == nil || st.UpdatedAt.IsZero() {
		return !sinceTime.IsZero() && now.Sub(sinceTime) > heartbeatGrace
	}
	return now.Sub(st.UpdatedAt) > heartbeatGrace
}

// emitHeartbeatAlert publishes the operator alert for a liveness edge. Both types
// are audited (ledgered) so the alert survives in the operator's audit log, not
// just the live event stream.
func (s *Server) emitHeartbeatAlert(tr heartbeatTransition) {
	typ := events.TypeAgentRecovered
	reason := "heartbeat returned"
	if tr.silent {
		typ = events.TypeAgentSilent
		reason = "no agent-state heartbeat past grace (stalled agent or skewed shim — check `dejima doctor` / upgrade)"
	}
	s.log.Warn("heartbeat monitor: "+string(typ), "island", tr.island, "agent", tr.agent)
	s.emit(events.Event{Type: typ, Island: tr.island, Agent: tr.agent, Payload: map[string]any{"reason": reason}})
}
