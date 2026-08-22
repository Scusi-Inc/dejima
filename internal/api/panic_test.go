package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// TestPanicEngageAndClear covers the emergency brake: POST /v1/panic stops every
// running island and sets the flag; GET reports it; DELETE clears it.
func TestPanicEngageAndClear(t *testing.T) {
	h, f := newTestServer(t) // HOME → temp dir
	for _, n := range []string{"one", "two"} {
		if err := (&project.Project{Name: n, DesiredState: project.StateRunning}).Save(); err != nil {
			t.Fatal(err)
		}
	}

	rr := do(t, h, http.MethodPost, "/v1/panic", `{"reason":"fire drill"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("engage: %d %s", rr.Code, rr.Body.String())
	}
	var resp PanicResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.Panicked || resp.Affected != 2 {
		t.Errorf("engage resp = %+v, want panicked, affected 2", resp)
	}
	if f.stopCalls != 2 {
		t.Errorf("stopCalls = %d, want 2", f.stopCalls)
	}
	if !panicEngaged() {
		t.Error("panicEngaged() = false after engage")
	}

	// Status endpoint + overview both reflect it.
	rr = do(t, h, http.MethodGet, "/v1/panic", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.Panicked || resp.Reason == "" {
		t.Errorf("status = %+v, want panicked with reason", resp)
	}
	rr = do(t, h, http.MethodGet, "/v1/overview", "")
	var ov OverviewResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ov)
	if !ov.Panicked {
		t.Error("overview.Panicked = false while engaged")
	}

	// Clear restores: flag gone, islands restarted.
	rr = do(t, h, http.MethodDelete, "/v1/panic", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Panicked {
		t.Error("clear resp still panicked")
	}
	if panicEngaged() {
		t.Error("panicEngaged() = true after clear")
	}
}

// TestAdoptSkipsWhenPanicked verifies the flag survives a daemon restart: while
// it's set, AdoptExisting starts nothing; once cleared, it starts the island.
func TestAdoptSkipsWhenPanicked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (&project.Project{Name: "isl", DesiredState: project.StateRunning}).Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{status: runtime.StatusStopped} // container down, desired up
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))

	if err := writePanicFlag("test"); err != nil {
		t.Fatal(err)
	}
	srv.AdoptExisting(context.Background())
	if f.startCalls != 0 {
		t.Errorf("startCalls = %d while panicked, want 0", f.startCalls)
	}

	if err := removePanicFlag(); err != nil {
		t.Fatal(err)
	}
	srv.AdoptExisting(context.Background())
	if f.startCalls == 0 {
		t.Error("startCalls = 0 after clearing panic, want the island started")
	}
}

// Panic must not touch DesiredState, and clearing it must restart exactly the
// islands that were MEANT to be running.
//
// The behaviour was correct and nothing held it there: making handlePanic write
// StateHibernated passed every test in this file. That matters beyond a
// refactor, because an API client cannot see the difference — "restart the
// islands that should be running" is only implementable if panic preserves the
// intent, so a client counts on it and reports that count to a human. If panic
// ever flipped the state, unpanic would restart nothing, report zero, and be
// indistinguishable from a fleet that was already stopped.
//
// A hibernated island is the case that proves it: it must stay hibernated
// through a panic AND must not be woken by clearing one.
func TestPanicPreservesDesiredState(t *testing.T) {
	h, _ := newTestServer(t)
	for name, want := range map[string]project.State{
		"runner":  project.StateRunning,
		"sleeper": project.StateHibernated,
	} {
		if err := (&project.Project{Name: name, DesiredState: want}).Save(); err != nil {
			t.Fatal(err)
		}
	}

	if rr := do(t, h, http.MethodPost, "/v1/panic", `{"reason":"drill"}`); rr.Code != http.StatusOK {
		t.Fatalf("engage: %d %s", rr.Code, rr.Body.String())
	}
	for name, want := range map[string]project.State{
		"runner":  project.StateRunning,
		"sleeper": project.StateHibernated,
	} {
		p, err := project.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if p.DesiredState != want {
			t.Errorf("panic changed %s's DesiredState to %q, want %q — clearing panic "+
				"restores islands to what they were MEANT to be, and it can only do "+
				"that if panic leaves the intent alone", name, p.DesiredState, want)
		}
	}

	// And clearing it restarts only the one that should be running.
	rr := do(t, h, http.MethodDelete, "/v1/panic", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rr.Code, rr.Body.String())
	}
	var resp PanicResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Affected != 1 {
		t.Errorf("clear restarted %d islands, want 1 — the hibernated one must stay "+
			"asleep, and a client reports this count to a human", resp.Affected)
	}
}
