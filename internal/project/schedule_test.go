package project

import (
	"testing"
	"time"
)

func TestWakeSchedule_DueAndAdvance(t *testing.T) {
	now := time.Now()

	// One-shot: due when NextDue has passed; AdvanceAfterFire returns false (drop).
	one := WakeSchedule{ID: "sched-1", NextDue: now.Add(-time.Minute)}
	if !one.Due(now) {
		t.Error("a past one-shot should be due")
	}
	if one.AdvanceAfterFire(now) {
		t.Error("a one-shot should not survive AdvanceAfterFire")
	}
	if one.LastRun.IsZero() {
		t.Error("AdvanceAfterFire should stamp LastRun")
	}

	// Recurring: AdvanceAfterFire returns true and pushes NextDue ~one interval
	// out FROM now (catch-up, not stack — even though it was long overdue).
	rec := WakeSchedule{ID: "sched-2", Every: "1h", NextDue: now.Add(-5 * time.Hour)}
	if !rec.Due(now) {
		t.Error("an overdue recurring schedule should be due")
	}
	if !rec.AdvanceAfterFire(now) {
		t.Error("a recurring schedule should survive AdvanceAfterFire")
	}
	if got := rec.NextDue.Sub(now); got < 59*time.Minute || got > 61*time.Minute {
		t.Errorf("recurring NextDue should be ~1h from now (catch-up), got %v", got)
	}

	// Not-yet-due schedule.
	future := WakeSchedule{NextDue: now.Add(time.Hour)}
	if future.Due(now) {
		t.Error("a future schedule should not be due")
	}
}

func TestProject_ScheduleAddRemove(t *testing.T) {
	p := &Project{Name: "isl"}
	a := p.AddSchedule(WakeSchedule{Every: "720h"})
	b := p.AddSchedule(WakeSchedule{Every: "24h"})
	if a.ID != "sched-1" || b.ID != "sched-2" {
		t.Fatalf("ids = %q, %q; want sched-1, sched-2", a.ID, b.ID)
	}
	if !p.RemoveSchedule("sched-1") {
		t.Fatal("RemoveSchedule should report a hit")
	}
	if len(p.Schedules) != 1 || p.Schedules[0].ID != "sched-2" {
		t.Fatalf("after remove: %+v", p.Schedules)
	}
	if p.RemoveSchedule("nope") {
		t.Error("removing an unknown id should report no hit")
	}
	// A fresh id doesn't collide with the still-present sched-2.
	c := p.AddSchedule(WakeSchedule{})
	if c.ID != "sched-3" {
		t.Errorf("next id = %q, want sched-3", c.ID)
	}
}
