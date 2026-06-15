package api

import (
	"context"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/version"
)

// DefaultWatchdogInterval is how often the container watchdog samples island
// health when started without an explicit interval.
const DefaultWatchdogInterval = 30 * time.Second

// watchState is the last-seen container health for one island, used to detect
// edges (running→stopped) and restart-count climbs between scans.
type watchState struct {
	running      bool
	restartCount int
}

// EmitDaemonStarted fires a daemon.started event carrying the build version and
// the active listen modes. On a headless host this is the only push-shaped way
// to learn the box rebooted or the daemon crashed and was restarted by its
// supervisor; it pairs with container.crashed from the watchdog.
func (s *Server) EmitDaemonStarted(listen []string) {
	s.emit(events.Event{
		Type: events.TypeDaemonStarted,
		Payload: map[string]any{
			"version":     version.Version,
			"api_version": version.APIVersion,
			"listen":      listen,
		},
	})
}

// RunWatchdog polls island container health on an interval and emits
// container.crashed when an island that should be running has exited
// unexpectedly, or its container restart count climbs (flapping under the
// restart policy). It only observes — it never starts or stops containers, so
// it can't fight AdoptExisting. Returns when ctx is cancelled; run it in its
// own goroutine.
func (s *Server) RunWatchdog(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultWatchdogInterval
	}
	last := map[string]watchState{}
	// Prime state on the first pass without emitting, so we don't fire for
	// pre-existing exits the operator already knows about (adopt logged them).
	s.scanWatchdog(ctx, last, false)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanWatchdog(ctx, last, true)
		}
	}
}

// scanWatchdog samples every desired-running island once, updating last and (when
// emit is true) firing container.crashed on a detected crash or flap.
func (s *Server) scanWatchdog(ctx context.Context, last map[string]watchState, emit bool) {
	if panicEngaged() {
		return // we deliberately stopped everything; not a crash
	}
	projects, err := project.List()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, p := range projects {
		seen[p.Name] = true
		if p.DesiredState != project.StateRunning {
			delete(last, p.Name)
			continue
		}
		status, err := s.rt.Status(ctx, p.ContainerName())
		if err != nil {
			continue
		}
		var health runtime.Health
		if h, err := s.rt.Inspect(ctx, p.ContainerName()); err == nil {
			health = h
		}
		running := status == runtime.StatusRunning
		prev, had := last[p.Name]
		if emit && had {
			switch {
			case prev.running && !running:
				// Was up, now down: an unexpected exit the operator didn't ask for.
				s.emit(events.Event{
					Type:   events.TypeContainerCrashed,
					Island: p.Name,
					Payload: map[string]any{
						"status":        string(status),
						"exit_code":     health.ExitCode,
						"oom_killed":    health.OOMKilled,
						"restart_count": health.RestartCount,
						"reason":        crashReason(status, health),
					},
				})
			case running && health.RestartCount > prev.restartCount:
				// Still up but the restart count climbed: flapping under the
				// `--restart unless-stopped` policy (e.g. repeated OOM kills).
				s.emit(events.Event{
					Type:   events.TypeContainerCrashed,
					Island: p.Name,
					Payload: map[string]any{
						"status":        string(status),
						"oom_killed":    health.OOMKilled,
						"restart_count": health.RestartCount,
						"reason":        "restarted",
					},
				})
			}
		}
		last[p.Name] = watchState{running: running, restartCount: health.RestartCount}
	}
	// Forget islands that no longer exist so the map can't grow unbounded.
	for name := range last {
		if !seen[name] {
			delete(last, name)
		}
	}
}

// crashReason classifies why a desired-running container is no longer running.
func crashReason(status runtime.ContainerStatus, h runtime.Health) string {
	switch {
	case h.OOMKilled:
		return "oom_killed"
	case status == runtime.StatusMissing:
		return "container_missing"
	case h.ExitCode != 0:
		return "nonzero_exit"
	default:
		return "exited"
	}
}
