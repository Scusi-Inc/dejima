package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// SchedulerTickInterval is how often the daemon checks for due scheduled wakes.
// Minute granularity is plenty — cadences are hours/days.
const SchedulerTickInterval = time.Minute

// scheduleReadyTimeout bounds how long a scheduled task waits for the woken
// agent to reach a prompt before we give up and warn (rather than typing into a
// still-booting terminal).
const scheduleReadyTimeout = 90 * time.Second

// RunScheduler fires durable per-island scheduled wakes (project.Schedules). It
// runs unconditionally — independent of idle-hibernate — because the schedule is
// the always-on counterpart to hibernate: the daemon holds it and wakes the
// island on cadence. Runs in its own goroutine; returns when ctx is cancelled.
func (s *Server) RunScheduler(ctx context.Context) {
	t := time.NewTicker(SchedulerTickInterval)
	defer t.Stop()
	s.log.Info("scheduled-wake enabled", "tick", SchedulerTickInterval.String())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanSchedules(ctx, time.Now())
		}
	}
}

// scanSchedules fires every island that has at least one due schedule. The
// heavy lifting (wake + task delivery) happens in fireDueSchedules under the
// island lock; here we only cheaply find islands with due work.
func (s *Server) scanSchedules(ctx context.Context, now time.Time) {
	projects, err := project.List()
	if err != nil {
		return
	}
	for _, p := range projects {
		for i := range p.Schedules {
			if p.Schedules[i].Due(now) {
				s.fireDueSchedules(ctx, p.Name, now)
				break
			}
		}
	}
}

// fireDueSchedules wakes an island and fires each of its due schedules: emit the
// ledgered scheduled-wake event, advance recurring schedules (catch-up, not
// stack) / drop one-shots, then deliver any tasks. Task delivery waits for the
// agent to be ready, so it runs OUTSIDE the island lock (in its own goroutine)
// to avoid blocking the scan.
func (s *Server) fireDueSchedules(ctx context.Context, name string, now time.Time) {
	lock := s.projectLock(name)
	lock.Lock()

	p, err := project.Load(name)
	if err != nil {
		lock.Unlock()
		return
	}

	type pendingTask struct{ agent, task string }
	var pending []pendingTask
	var firedIDs []string
	kept := make([]project.WakeSchedule, 0, len(p.Schedules))
	anyDue := false
	for _, sc := range p.Schedules {
		if !sc.Due(now) {
			kept = append(kept, sc)
			continue
		}
		anyDue = true
		firedIDs = append(firedIDs, sc.ID)
		if sc.Task != "" {
			pending = append(pending, pendingTask{agent: sc.Agent, task: sc.Task})
		}
		if sc.AdvanceAfterFire(now) { // recurring → keep with the next NextDue; one-shot → drop
			kept = append(kept, sc)
		}
	}
	if !anyDue {
		lock.Unlock()
		return
	}
	// Bring the island up if it's hibernated (no-op if already running); a
	// scheduled wake on an already-running island still fires its task.
	s.startIslandIfStopped(ctx, p, now)
	p.Schedules = kept
	if err := p.Save(); err != nil {
		s.log.Warn("scheduled-wake: save", "island", name, "err", err)
	}
	lock.Unlock()

	s.emit(events.Event{Type: events.TypeIslandWoken, Island: name, Payload: map[string]any{
		"reason": "scheduled", "schedules": firedIDs,
	}})

	for _, pt := range pending {
		go s.deliverScheduledTask(ctx, name, pt.agent, pt.task)
	}
}

// startIslandIfStopped starts (or recreates) an island's container when it isn't
// running and relaunches its agents. The caller MUST hold the island lock — this
// mirrors wakeIslandFor's core without taking the lock itself (wakeIslandFor
// takes it), so the scheduler can wake under the lock it already holds.
func (s *Server) startIslandIfStopped(ctx context.Context, p *project.Project, now time.Time) {
	status, err := s.rt.Status(ctx, p.ContainerName())
	if err != nil || status == runtime.StatusRunning {
		return
	}
	switch status {
	case runtime.StatusMissing:
		if err := s.createContainerForProject(ctx, p, "", false); err != nil {
			s.log.Warn("scheduled-wake: recreate", "island", p.Name, "err", err)
			return
		}
	default:
		if err := s.rt.StartContainer(ctx, p.ContainerName()); err != nil {
			s.log.Warn("scheduled-wake: start", "island", p.Name, "err", err)
			return
		}
	}
	p.DesiredState = project.StateRunning
	p.LastUsedAt = now.UTC()
	s.reconcileAgentsAsync(p, false) // the entrypoint relaunches the primary; restore the rest
}

// deliverScheduledTask injects a schedule's task into the target agent once it's
// ready (at a turn boundary), giving a just-woken agent time to boot. On timeout
// it warns rather than typing into a dead terminal. Runs outside the island lock.
func (s *Server) deliverScheduledTask(ctx context.Context, island, agentRef, task string) {
	deadline := time.Now().Add(scheduleReadyTimeout)
	for {
		p, err := project.Load(island)
		if err != nil {
			return
		}
		a := scheduleTargetAgent(p, agentRef)
		if a == nil {
			s.log.Warn("scheduled-wake: no target agent for task", "island", island, "agent", agentRef)
			return
		}
		if s.agentIdleAtBoundary(island, a.ID) {
			if err := s.injectFn(ctx, p, a, task); err != nil {
				s.log.Warn("scheduled-wake: inject task", "island", island, "agent", a.ID, "err", err)
			}
			return
		}
		if time.Now().After(deadline) {
			s.log.Warn("scheduled-wake: agent not ready — task not delivered", "island", island, "agent", a.ID)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// scheduleTargetAgent resolves a schedule's Agent field (id or label; "" = the
// primary) to a concrete agent spec, or nil if none matches.
func scheduleTargetAgent(p *project.Project, ref string) *project.AgentSpec {
	if ref == "" {
		return p.PrimaryAgent()
	}
	a, err := p.ResolveAgentRef(ref) // id wins, else label; nil on no/ambiguous match
	if err != nil {
		return nil
	}
	return a
}

// --- HTTP handlers ---------------------------------------------------------

// createSchedule adds a wake schedule to an island. Operator-only.
func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	sc, err := scheduleFromRequest(req, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sc = p.AddSchedule(sc)
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScheduleInfo(sc))
}

// listSchedules returns an island's wake schedules.
func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	out := make([]ScheduleInfo, 0, len(p.Schedules))
	for _, sc := range p.Schedules {
		out = append(out, toScheduleInfo(sc))
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteSchedule removes a wake schedule by id. Operator-only.
func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !p.RemoveSchedule(id) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no schedule %q on island %q", id, name))
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scheduleFromRequest validates a create request and builds a WakeSchedule with
// its first NextDue: a one-shot fires at `at`; a recurring one fires a first
// interval out from now.
func scheduleFromRequest(req CreateScheduleRequest, now time.Time) (project.WakeSchedule, error) {
	sc := project.WakeSchedule{Task: req.Task, Agent: req.Agent, CreatedAt: now.UTC()}
	switch {
	case req.Every != "":
		d, err := time.ParseDuration(req.Every)
		if err != nil || d <= 0 {
			return sc, fmt.Errorf("invalid --every %q: want a positive Go duration like 720h", req.Every)
		}
		sc.Every = req.Every
		sc.NextDue = now.Add(d).UTC()
	case req.At != "":
		t, err := time.Parse(time.RFC3339, req.At)
		if err != nil {
			return sc, fmt.Errorf("invalid --at %q: want an RFC3339 time like 2026-07-01T09:00:00Z", req.At)
		}
		sc.NextDue = t.UTC()
	default:
		return sc, fmt.Errorf("a schedule needs --every <duration> or --at <RFC3339>")
	}
	return sc, nil
}

func toScheduleInfo(sc project.WakeSchedule) ScheduleInfo {
	return ScheduleInfo{
		ID:        sc.ID,
		Every:     sc.Every,
		Task:      sc.Task,
		Agent:     sc.Agent,
		NextDue:   sc.NextDue,
		LastRun:   sc.LastRun,
		CreatedAt: sc.CreatedAt,
	}
}
