package api

import (
	"context"
	"fmt"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// DefaultIdleScanInterval bounds how often the idle-hibernator samples islands.
// The actual cadence is min(this, threshold/4) so a short threshold still gets
// checked a few times within its window.
const DefaultIdleScanInterval = 5 * time.Minute

// RunIdleHibernator hibernates running islands that have been idle — no attached
// client and no live agent process — for at least `threshold`. It is opt-in:
// threshold <= 0 disables it (the default), and it returns immediately.
//
// It only ever *stops* an idle island (reclaiming its slice of the runtime's
// memory); it never deletes anything, and it never touches an island with a live
// agent process — so a working agent or an idling-but-alive Home Island brain is
// safe. Run it in its own goroutine; it returns when ctx is cancelled.
func (s *Server) RunIdleHibernator(ctx context.Context, threshold time.Duration) {
	if threshold <= 0 {
		return
	}
	scan := DefaultIdleScanInterval
	if q := threshold / 4; q < scan {
		scan = q
	}
	if scan < time.Minute {
		scan = time.Minute
	}
	s.log.Info("idle auto-hibernate enabled", "threshold", threshold.String(), "scan", scan.String())

	idleSince := map[string]time.Time{}
	t := time.NewTicker(scan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanIdle(ctx, idleSince, threshold)
		}
	}
}

// scanIdle is one sweep: it tracks how long each island has been continuously
// idle and hibernates those past the threshold. idleSince persists across sweeps
// (the caller owns it) so the clock survives between ticks but resets to "not
// idle" the moment a client attaches or an agent comes alive.
func (s *Server) scanIdle(ctx context.Context, idleSince map[string]time.Time, threshold time.Duration) {
	projects, err := project.List()
	if err != nil {
		return
	}
	now := time.Now()
	seen := map[string]bool{}
	for _, p := range projects {
		seen[p.Name] = true
		// Pinned-awake islands are exempt: never idle-hibernate them (a persistent
		// ambient agent must survive its idle windows). Reset the clock so unpinning
		// later starts the idle timer fresh rather than hibernating on the next tick.
		if p.NoHibernate {
			delete(idleSince, p.Name)
			continue
		}
		status, _ := s.rt.Status(ctx, p.ContainerName())
		if status != runtime.StatusRunning || !s.islandIdle(ctx, p) {
			delete(idleSince, p.Name) // running+busy, or stopped: reset the idle clock
			continue
		}
		since, ok := idleSince[p.Name]
		if !ok {
			idleSince[p.Name] = now // first observed idle this run
			continue
		}
		if now.Sub(since) < threshold {
			continue
		}
		if err := s.hibernateIdle(ctx, p.Name); err != nil {
			s.log.Warn("idle hibernate", "island", p.Name, "err", err)
		} else {
			s.log.Info("idle hibernate", "island", p.Name, "idle_for", now.Sub(since).Round(time.Minute).String())
		}
		delete(idleSince, p.Name)
	}
	// Forget islands that no longer exist so the map can't grow unbounded.
	for name := range idleSince {
		if !seen[name] {
			delete(idleSince, name)
		}
	}
}

// islandIdle reports whether an island has no attached clients and no live agent
// process — the two conditions that make it safe to hibernate.
func (s *Server) islandIdle(ctx context.Context, p *project.Project) bool {
	if len(s.islandPresence(p.Name)) > 0 {
		return false
	}
	p.EnsureAgents() // back-fill Agents[0] from the legacy scalar for old islands
	for i := range p.Agents {
		if s.agentLiveness(ctx, p, &p.Agents[i]) == "running" {
			return false
		}
	}
	return true
}

// hibernateIdle stops an idle island under its lock, re-checking idleness so it
// can't race a just-attached client or a concurrent wake. Mirrors hibernateIsland
// minus the HTTP plumbing.
func (s *Server) hibernateIdle(ctx context.Context, name string) error {
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		return err
	}
	if status, _ := s.rt.Status(ctx, p.ContainerName()); status != runtime.StatusRunning {
		return nil
	}
	if !s.islandIdle(ctx, p) { // re-check under the lock
		return nil
	}
	if err := s.rt.StopContainer(ctx, p.ContainerName()); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	p.DesiredState = project.StateHibernated
	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		return err
	}
	s.emit(events.Event{Type: events.TypeIslandHibernated, Island: p.Name, Payload: map[string]any{"reason": "idle"}})
	return nil
}
