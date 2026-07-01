package api

import (
	"context"
	"testing"
	"time"
)

// heartbeatSilent classifies liveness: a hibernated island is never silent; a
// running one is silent when it never heartbeat past boot-grace, or its last
// heartbeat is older than the grace.
func TestHeartbeatSilent(t *testing.T) {
	now := time.Now()
	boot := now.Add(-30 * time.Minute) // long enough that grace has elapsed
	fresh := &AgentStateInfo{UpdatedAt: now.Add(-time.Minute)}
	stale := &AgentStateInfo{UpdatedAt: now.Add(-30 * time.Minute)}
	never := &AgentStateInfo{} // present but zero UpdatedAt

	cases := []struct {
		name      string
		running   bool
		st        *AgentStateInfo
		sinceTime time.Time
		want      bool
	}{
		{"hibernated is never silent", false, stale, boot, false},
		{"fresh heartbeat → not silent", true, fresh, boot, false},
		{"stale heartbeat → silent", true, stale, boot, true},
		{"never heard, past grace → silent", true, nil, boot, true},
		{"never heard, zero UpdatedAt, past grace → silent", true, never, boot, true},
		{"never heard, just booted → not silent", true, nil, now.Add(-time.Minute), false},
		{"never heard, zero sinceTime → not silent", true, nil, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := heartbeatSilent(c.running, c.st, c.sinceTime, now); got != c.want {
				t.Fatalf("heartbeatSilent = %v, want %v", got, c.want)
			}
		})
	}
}

// scanHeartbeats edge-triggers: it emits a transition when an island goes silent,
// stays quiet while it remains silent (alert once, not every tick), and emits a
// recovery when the heartbeat returns.
func TestScanHeartbeats_EdgeTriggers(t *testing.T) {
	srv, h, _ := wakeServer(t)
	createIsland(t, h, "isl")
	agent := primaryAgentID(t, h, "isl")
	now := time.Now()

	setHeartbeat := func(updated time.Time) {
		srv.agentStateMu.Lock()
		srv.agentStates[agentStateKey("isl", agent)] = AgentStateInfo{Latest: "thinking", UpdatedAt: updated}
		srv.agentStateMu.Unlock()
	}

	silent := map[string]bool{}

	// Stale heartbeat → newly silent (one transition, silent=true).
	setHeartbeat(now.Add(-30 * time.Minute))
	tr := srv.scanHeartbeats(context.Background(), silent, now)
	if len(tr) != 1 || !tr[0].silent || tr[0].island != "isl" {
		t.Fatalf("expected one silent transition, got %+v", tr)
	}
	if tr[0].agent != agent {
		t.Errorf("transition agent = %q, want %q", tr[0].agent, agent)
	}

	// Still silent → NO new transition (edge-triggered).
	if tr := srv.scanHeartbeats(context.Background(), silent, now); len(tr) != 0 {
		t.Fatalf("ongoing silence should not re-alert, got %+v", tr)
	}

	// Heartbeat returns → recovery transition (silent=false).
	setHeartbeat(now)
	tr = srv.scanHeartbeats(context.Background(), silent, now)
	if len(tr) != 1 || tr[0].silent {
		t.Fatalf("expected one recovery transition, got %+v", tr)
	}
	if silent["isl"] {
		t.Error("recovered island should be cleared from the silent set")
	}
}

// A just-created, running island with no heartbeat yet must NOT alert (it's
// within the boot grace) — no false positive on startup.
func TestScanHeartbeats_NoFalseAlarmOnFreshIsland(t *testing.T) {
	srv, h, _ := wakeServer(t)
	createIsland(t, h, "isl")
	if tr := srv.scanHeartbeats(context.Background(), map[string]bool{}, time.Now()); len(tr) != 0 {
		t.Fatalf("a fresh island should not alert, got %+v", tr)
	}
}
