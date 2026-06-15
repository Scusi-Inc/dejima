package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

func newWatchdogServer(t *testing.T, f *fakeRuntime) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := (&project.Project{Name: "isl", DesiredState: project.StateRunning}).Save(); err != nil {
		t.Fatal(err)
	}
	return NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func crashedEvents(s *Server) int {
	n := 0
	for _, e := range s.IslandEvents("isl") {
		if e.Type == events.TypeContainerCrashed {
			n++
		}
	}
	return n
}

// TestWatchdogDetectsUnexpectedExit: a desired-running island that goes from
// running to stopped emits exactly one container.crashed, and the priming pass
// never emits.
func TestWatchdogDetectsUnexpectedExit(t *testing.T) {
	f := &fakeRuntime{status: runtime.StatusRunning, health: runtime.Health{ExitCode: 137, OOMKilled: true}}
	s := newWatchdogServer(t, f)
	ctx := context.Background()
	last := map[string]watchState{}

	// Priming pass (emit=false): records baseline, never fires.
	s.scanWatchdog(ctx, last, false)
	if got := crashedEvents(s); got != 0 {
		t.Fatalf("priming pass emitted %d crash events, want 0", got)
	}

	// Container exits.
	f.status = runtime.StatusStopped
	s.scanWatchdog(ctx, last, true)
	if got := crashedEvents(s); got != 1 {
		t.Fatalf("after exit: %d crash events, want 1", got)
	}
	// Reason should reflect the OOM kill.
	ev := s.IslandEvents("isl")[0]
	if ev.Payload["reason"] != "oom_killed" {
		t.Errorf("reason = %v, want oom_killed", ev.Payload["reason"])
	}

	// A subsequent scan with no further change must not re-emit (edge-triggered).
	s.scanWatchdog(ctx, last, true)
	if got := crashedEvents(s); got != 1 {
		t.Errorf("steady-state re-emitted: %d crash events, want 1", got)
	}
}

// TestWatchdogDetectsFlap: a container that stays up but whose restart count
// climbs is flapping under the restart policy and emits container.crashed.
func TestWatchdogDetectsFlap(t *testing.T) {
	f := &fakeRuntime{status: runtime.StatusRunning, health: runtime.Health{RestartCount: 1}}
	s := newWatchdogServer(t, f)
	ctx := context.Background()
	last := map[string]watchState{}

	s.scanWatchdog(ctx, last, false) // baseline restartCount=1
	f.health = runtime.Health{RestartCount: 3}
	s.scanWatchdog(ctx, last, true)
	if got := crashedEvents(s); got != 1 {
		t.Fatalf("flap: %d crash events, want 1", got)
	}
	if ev := s.IslandEvents("isl")[0]; ev.Payload["reason"] != "restarted" {
		t.Errorf("reason = %v, want restarted", ev.Payload["reason"])
	}
}

// TestWatchdogSilentWhenPanicked: panic stops everything deliberately, so the
// watchdog must not report those stops as crashes.
func TestWatchdogSilentWhenPanicked(t *testing.T) {
	f := &fakeRuntime{status: runtime.StatusRunning}
	s := newWatchdogServer(t, f)
	ctx := context.Background()
	last := map[string]watchState{}

	s.scanWatchdog(ctx, last, false)
	if err := writePanicFlag("drill"); err != nil {
		t.Fatal(err)
	}
	f.status = runtime.StatusStopped
	s.scanWatchdog(ctx, last, true)
	if got := crashedEvents(s); got != 0 {
		t.Errorf("emitted %d crash events while panicked, want 0", got)
	}
}
