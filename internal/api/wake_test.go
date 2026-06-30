package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/mailbox"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

func wakeServer(t *testing.T) (*Server, http.Handler, *fakeRuntime) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return srv, srv.Handler(), f
}

// TestWakeNotifierBatching: arrivals accumulate per recipient and reset on take
// (one nudge per quiet period, not one per message).
func TestWakeNotifierBatching(t *testing.T) {
	n := newWakeNotifier()
	now := time.Now()
	n.add("isl", "a1", now)
	n.add("isl", "a1", now)
	n.add("isl", "a2", now)
	if len(n.keys()) != 2 {
		t.Fatalf("keys = %d, want 2", len(n.keys()))
	}
	if c := n.take(nudgeKey{"isl", "a1"}); c != 2 {
		t.Errorf("take(a1) = %d, want 2 (batched)", c)
	}
	if c := n.take(nudgeKey{"isl", "a1"}); c != 0 {
		t.Errorf("take(a1) again = %d, want 0 (cleared)", c)
	}
}

// TestWakeFlushTurnBoundary: a busy agent is never injected mid-turn; once it's
// at a turn boundary the batched nudge is delivered exactly once (de-duped).
func TestWakeFlushTurnBoundary(t *testing.T) {
	srv, h, _ := wakeServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"isl","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	agent := primaryAgentID(t, h, "isl")

	var injected []string
	srv.injectFn = func(_ context.Context, _ *project.Project, _ *project.AgentSpec, text string) error {
		injected = append(injected, text)
		return nil
	}
	idle := false
	srv.idleFn = func(string, string) bool { return idle }

	srv.wakeNudges.add("isl", agent, time.Now())
	srv.flushNudges(context.Background())
	if len(injected) != 0 {
		t.Fatalf("busy agent was injected mid-turn: %v", injected)
	}

	srv.wakeNudges.add("isl", agent, time.Now()) // a second message arrives while busy → count 2
	idle = true
	srv.flushNudges(context.Background())
	if len(injected) != 1 || !strings.Contains(injected[0], "2 new") {
		t.Fatalf("expected one batched nudge of 2; got %v", injected)
	}
	srv.flushNudges(context.Background()) // de-dupe: nothing pending now
	if len(injected) != 1 {
		t.Errorf("nudge re-sent with nothing pending: %v", injected)
	}
}

// TestWakeFlushStuckNoHeartbeat: a nudge stuck past the grace window is held when
// the agent has a fresh heartbeat (protect live work), but delivered best-effort
// once that heartbeat goes stale/absent (e.g. a stale shim that can't POST
// agent-state — version skew) so the recipient still learns it has mail.
func TestWakeFlushStuckNoHeartbeat(t *testing.T) {
	srv, h, _ := wakeServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"isl","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	agent := primaryAgentID(t, h, "isl")

	var injected []string
	srv.injectFn = func(_ context.Context, _ *project.Project, _ *project.AgentSpec, text string) error {
		injected = append(injected, text)
		return nil
	}
	srv.idleFn = func(string, string) bool { return false } // never reports a turn boundary

	// Stuck nudge, but the agent has a FRESH heartbeat (actively turning): hold —
	// force-delivery would clobber live work.
	srv.agentStateMu.Lock()
	srv.agentStates[agentStateKey("isl", agent)] = AgentStateInfo{Latest: "thinking", UpdatedAt: time.Now()}
	srv.agentStateMu.Unlock()
	srv.wakeNudges.add("isl", agent, time.Now().Add(-2*wakeStuckGrace))
	srv.flushNudges(context.Background())
	if len(injected) != 0 {
		t.Fatalf("stuck nudge force-delivered to an agent with a fresh heartbeat: %v", injected)
	}

	// Heartbeat goes stale (shim stopped POSTing — skew): now deliver best-effort.
	srv.agentStateMu.Lock()
	srv.agentStates[agentStateKey("isl", agent)] = AgentStateInfo{Latest: "thinking", UpdatedAt: time.Now().Add(-2 * wakeHeartbeatStale)}
	srv.agentStateMu.Unlock()
	srv.flushNudges(context.Background())
	if len(injected) != 1 || !strings.Contains(injected[0], "new message") {
		t.Fatalf("stuck nudge to a stale-heartbeat agent not delivered: %v", injected)
	}
}

// TestOnArrivalWakesHibernated: an arrival to a hibernated island wakes it (the
// actor-model wake half) so the recipient can later be nudged.
func TestOnArrivalWakesHibernated(t *testing.T) {
	srv, h, f := wakeServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"isl","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	agent := primaryAgentID(t, h, "isl")

	srv.idleFn = func(string, string) bool { return false } // don't inject; only test the wake
	f.mu.Lock()
	f.status = runtime.StatusExited // simulate hibernated/stopped
	before := f.startCalls
	f.mu.Unlock()

	srv.onMailboxArrival(mailbox.Message{Island: "isl", To: agent})

	f.mu.Lock()
	after := f.startCalls
	f.mu.Unlock()
	if after == before {
		t.Fatal("arrival to a hibernated island did not wake it (no StartContainer)")
	}
}

// TestArrivalEmitsEvent: every arrival emits mailbox.arrival (the wrapper-policy
// seam), regardless of soft-notify.
func TestArrivalEmitsEvent(t *testing.T) {
	srv, h, _ := wakeServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"isl","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	srv.SetWakeNotify(false) // even with soft-notify off, the event must fire
	srv.idleFn = func(string, string) bool { return false }
	srv.onMailboxArrival(mailbox.Message{Island: "isl", To: "a1", From: "a2"})

	found := false
	for _, e := range srv.IslandEvents("isl") {
		if e.Type == "mailbox.arrival" {
			found = true
		}
	}
	if !found {
		t.Error("mailbox.arrival event not recorded")
	}
}
