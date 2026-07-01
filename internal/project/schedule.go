package project

import (
	"fmt"
	"time"
)

// WakeSchedule is a durable, daemon-owned instruction to wake an island on a
// cadence (or once), optionally running a task on the agent. Stored in the
// island's config.toml so it survives daemon restart AND `dejima upgrade` — the
// whole point versus an in-island cron, which dies on container recreate.
type WakeSchedule struct {
	ID string `toml:"id"`
	// Every is a Go duration string ("720h") for a recurring wake; "" = one-shot.
	Every string `toml:"every,omitempty"`
	// Task is an optional prompt/command injected into the agent on wake; "" just
	// wakes the island (what then runs is the agent's own business).
	Task string `toml:"task,omitempty"`
	// Agent selects which agent runs Task (id or label); "" = the island's primary.
	Agent string `toml:"agent,omitempty"`
	// NextDue is when the scheduler should next fire this (UTC).
	NextDue time.Time `toml:"next_due"`
	// LastRun is the last time it fired (UTC); zero until the first fire. No
	// omitempty: go-toml/v2's omitempty drops a non-zero time.Time, which would
	// silently lose the last-run stamp across a save/load.
	LastRun   time.Time `toml:"last_run"`
	CreatedAt time.Time `toml:"created_at"`
}

// EveryDuration parses Every into a positive duration; ok=false for a one-shot
// (empty or unparseable Every).
func (w WakeSchedule) EveryDuration() (time.Duration, bool) {
	if w.Every == "" {
		return 0, false
	}
	d, err := time.ParseDuration(w.Every)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// Recurring reports whether this schedule repeats.
func (w WakeSchedule) Recurring() bool {
	_, ok := w.EveryDuration()
	return ok
}

// Due reports whether the schedule should fire at now.
func (w WakeSchedule) Due(now time.Time) bool {
	return !w.NextDue.IsZero() && !now.Before(w.NextDue)
}

// AdvanceAfterFire moves a recurring schedule's NextDue forward FROM now
// (catch-up, not stack: anchor to now+Every rather than replaying every missed
// interval). Returns false for a one-shot — the caller deletes it after firing.
func (w *WakeSchedule) AdvanceAfterFire(now time.Time) bool {
	d, ok := w.EveryDuration()
	w.LastRun = now.UTC()
	if !ok {
		return false
	}
	w.NextDue = now.Add(d).UTC()
	return true
}

// NextScheduleID returns an island-unique schedule id ("sched-1", "sched-2", …),
// mirroring NextAgentID.
func (p *Project) NextScheduleID() string {
	max := 0
	for _, s := range p.Schedules {
		var n int
		if _, err := fmt.Sscanf(s.ID, "sched-%d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("sched-%d", max+1)
}

// AddSchedule appends s (assigning an id if empty) and returns the stored value.
func (p *Project) AddSchedule(s WakeSchedule) WakeSchedule {
	if s.ID == "" {
		s.ID = p.NextScheduleID()
	}
	p.Schedules = append(p.Schedules, s)
	return s
}

// RemoveSchedule drops the schedule with the given id, reporting whether one was
// found and removed.
func (p *Project) RemoveSchedule(id string) bool {
	kept := make([]WakeSchedule, 0, len(p.Schedules))
	removed := false
	for _, s := range p.Schedules {
		if s.ID == id {
			removed = true
			continue
		}
		kept = append(kept, s)
	}
	p.Schedules = kept
	return removed
}
