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
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

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
