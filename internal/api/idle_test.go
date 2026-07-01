package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/project"
)

// createIsland is a small helper: POST a running island and fail on a non-201.
func createIsland(t *testing.T, h http.Handler, name string) {
	t.Helper()
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"`+name+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create %s: %d (%s)", name, rr.Code, rr.Body.String())
	}
}

func getIsland(t *testing.T, h http.Handler, name string) IslandInfo {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+name, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get %s: %d", name, rr.Code)
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode island: %v", err)
	}
	return info
}

// A no_hibernate PATCH must not clobber the title, and vice-versa — the reason
// UpdateIslandRequest's fields are pointers.
func TestUpdateIsland_NoHibernateDoesNotClobberTitle(t *testing.T) {
	_, h, _ := wakeServer(t)
	createIsland(t, h, "isl")

	// Set a title.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl", `{"title":"My Island"}`); rr.Code != http.StatusOK {
		t.Fatalf("set title: %d", rr.Code)
	}
	// Pin it — title-less PATCH must leave the title intact.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl", `{"no_hibernate":true}`); rr.Code != http.StatusOK {
		t.Fatalf("pin: %d", rr.Code)
	}
	info := getIsland(t, h, "isl")
	if !info.NoHibernate {
		t.Error("no_hibernate did not stick")
	}
	if info.Title != "My Island" {
		t.Errorf("title clobbered by the no_hibernate update: %q", info.Title)
	}

	// A title-only PATCH must leave no_hibernate intact.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl", `{"title":"Renamed"}`); rr.Code != http.StatusOK {
		t.Fatalf("rename: %d", rr.Code)
	}
	info = getIsland(t, h, "isl")
	if info.Title != "Renamed" {
		t.Errorf("title = %q, want Renamed", info.Title)
	}
	if !info.NoHibernate {
		t.Error("no_hibernate cleared by the title update")
	}

	// Unpin.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl", `{"no_hibernate":false}`); rr.Code != http.StatusOK {
		t.Fatalf("unpin: %d", rr.Code)
	}
	if getIsland(t, h, "isl").NoHibernate {
		t.Error("no_hibernate still set after unpin")
	}
}

// A pinned island is exempt from idle auto-hibernate even when long idle; the
// same island, once unpinned, hibernates on the next idle scan.
func TestScanIdle_SkipsPinned(t *testing.T) {
	srv, h, f := wakeServer(t)
	createIsland(t, h, "isl")

	// Pin, then run an idle scan with the clock already past the threshold.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl", `{"no_hibernate":true}`); rr.Code != http.StatusOK {
		t.Fatalf("pin: %d", rr.Code)
	}
	old := map[string]time.Time{"isl": time.Now().Add(-time.Hour)}
	srv.scanIdle(context.Background(), old, time.Minute)
	if f.stopCalls != 0 {
		t.Fatalf("pinned island was hibernated (stopCalls=%d)", f.stopCalls)
	}
	if p, _ := project.Load("isl"); p.DesiredState == project.StateHibernated {
		t.Fatal("pinned island's DesiredState went to hibernated")
	}

	// Unpin → an idle scan now hibernates it.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl", `{"no_hibernate":false}`); rr.Code != http.StatusOK {
		t.Fatalf("unpin: %d", rr.Code)
	}
	old = map[string]time.Time{"isl": time.Now().Add(-time.Hour)}
	srv.scanIdle(context.Background(), old, time.Minute)
	if p, _ := project.Load("isl"); p.DesiredState != project.StateHibernated {
		t.Fatalf("unpinned idle island was not hibernated (state=%s, stopCalls=%d)", p.DesiredState, f.stopCalls)
	}
}
