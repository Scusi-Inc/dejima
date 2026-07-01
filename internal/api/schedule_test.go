package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/project"
)

func listSchedules(t *testing.T, h http.Handler, island string) []ScheduleInfo {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+island+"/schedules", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list schedules: %d", rr.Code)
	}
	var out []ScheduleInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The /v1/islands/{name}/schedules handlers: create (recurring + one-shot +
// validation), list, delete.
func TestSchedule_CreateListDelete(t *testing.T) {
	_, h, _ := wakeServer(t)
	createIsland(t, h, "isl")

	// Neither every nor at → 400.
	if rr := do(t, h, http.MethodPost, "/v1/islands/isl/schedules", `{}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty schedule: want 400, got %d", rr.Code)
	}
	// Bad duration → 400.
	if rr := do(t, h, http.MethodPost, "/v1/islands/isl/schedules", `{"every":"soon"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad --every: want 400, got %d", rr.Code)
	}

	// Recurring, valid.
	rr := do(t, h, http.MethodPost, "/v1/islands/isl/schedules", `{"every":"720h","task":"run drift-check"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create recurring: %d (%s)", rr.Code, rr.Body.String())
	}
	var sc ScheduleInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &sc)
	if sc.ID == "" || sc.Every != "720h" || sc.Task != "run drift-check" {
		t.Fatalf("unexpected schedule: %+v", sc)
	}
	if sc.NextDue.Before(time.Now().Add(700 * time.Hour)) {
		t.Errorf("recurring NextDue should be ~720h out, got %s", sc.NextDue)
	}

	// One-shot with --at.
	at := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	if rr := do(t, h, http.MethodPost, "/v1/islands/isl/schedules", `{"at":"`+at+`"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create one-shot: %d", rr.Code)
	}

	if got := listSchedules(t, h, "isl"); len(got) != 2 {
		t.Fatalf("list: want 2, got %d", len(got))
	}

	// Delete the first; list drops to 1. Unknown id → 404.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/isl/schedules/"+sc.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodDelete, "/v1/islands/isl/schedules/nope", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: want 404, got %d", rr.Code)
	}
	if got := listSchedules(t, h, "isl"); len(got) != 1 {
		t.Fatalf("list after delete: want 1, got %d", len(got))
	}
}

// scanSchedules fires a due one-shot (which is then removed) and advances a due
// recurring schedule rather than deleting it.
func TestScanSchedules_FiresDueOneShotAndAdvancesRecurring(t *testing.T) {
	srv, h, _ := wakeServer(t)
	createIsland(t, h, "isl")

	// Seed two due schedules directly (no task → no async inject to wait on).
	lock := srv.projectLock("isl")
	lock.Lock()
	p, _ := project.Load("isl")
	now := time.Now()
	p.AddSchedule(project.WakeSchedule{NextDue: now.Add(-time.Minute)})                // one-shot, due
	p.AddSchedule(project.WakeSchedule{Every: "1h", NextDue: now.Add(-2 * time.Hour)}) // recurring, overdue
	_ = p.Save()
	lock.Unlock()

	srv.scanSchedules(context.Background(), now)

	got := listSchedules(t, h, "isl")
	if len(got) != 1 {
		t.Fatalf("after fire: want 1 (one-shot dropped, recurring kept), got %d: %+v", len(got), got)
	}
	rec := got[0]
	if rec.Every != "1h" {
		t.Fatalf("surviving schedule should be the recurring one, got %+v", rec)
	}
	if rec.NextDue.Before(now.Add(50 * time.Minute)) {
		t.Errorf("recurring NextDue should have advanced ~1h out, got %s", rec.NextDue)
	}
	if rec.LastRun.IsZero() {
		t.Error("fired recurring schedule should have LastRun stamped")
	}
}
