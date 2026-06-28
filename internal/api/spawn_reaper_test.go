package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/spawn"
)

func TestShouldReap(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	eph := func(spawnedBy string, age time.Duration) project.AgentSpec {
		return project.AgentSpec{ID: "x", Ephemeral: true, SpawnedBy: spawnedBy, CreatedAt: now.Add(-age)}
	}
	cases := []struct {
		name          string
		a             project.AgentSpec
		parentPresent bool
		ttl           time.Duration
		state         string
		want          bool
	}{
		{"non-ephemeral never reaped", project.AgentSpec{ID: "a1"}, true, time.Hour, "exited", false},
		{"parent gone", eph("a1", time.Minute), false, time.Hour, "running", true},
		{"parent present, fresh, running", eph("a1", time.Minute), true, time.Hour, "running", false},
		{"ttl expired", eph("a1", 2*time.Hour), true, time.Hour, "running", true},
		{"ttl zero never ages out", eph("a1", 100*time.Hour), true, 0, "running", false},
		{"exited", eph("a1", time.Minute), true, time.Hour, "exited", true},
		{"stopped", eph("a1", time.Minute), true, time.Hour, "stopped", true},
		{"root ephemeral (no parent) alive kept", eph("", time.Minute), false, time.Hour, "running", false},
	}
	for _, c := range cases {
		if got, _ := shouldReap(c.a, c.parentPresent, c.ttl, c.state, now); got != c.want {
			t.Errorf("%s: shouldReap = %v, want %v", c.name, got, c.want)
		}
	}
}

// saveProjectWithAgents writes a project straight to disk (bypassing the create
// API) so a test can stage ephemeral agents with controlled CreatedAt/lineage.
func saveProjectWithAgents(t *testing.T, name string, agents []project.AgentSpec) {
	t.Helper()
	p := &project.Project{Name: name, DesiredState: project.StateRunning, Agents: agents}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
}

func agentIDs(t *testing.T, name string) map[string]bool {
	t.Helper()
	p, err := project.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	p.EnsureAgents()
	ids := map[string]bool{}
	for _, a := range p.Agents {
		ids[a.ID] = true
	}
	return ids
}

func TestSpawnReaper_TTLAndParentGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Stopped container → agentsLive=false → no liveness exec → isolates the
	// TTL/parent triggers (state stays "").
	f := &fakeRuntime{status: runtime.StatusStopped}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	now := time.Now()
	saveProjectWithAgents(t, "alpha", []project.AgentSpec{
		{ID: "a1", Type: "claude-code"}, // root (non-ephemeral) — keep
		{ID: "a2", Type: "claude-code", Ephemeral: true, SpawnedBy: "a1", CreatedAt: now.Add(-2 * time.Hour)}, // TTL-expired — reap
		{ID: "a3", Type: "claude-code", Ephemeral: true, SpawnedBy: "gone", CreatedAt: now},                   // parent gone — reap
		{ID: "a4", Type: "claude-code", Ephemeral: true, SpawnedBy: "a1", CreatedAt: now},                     // fresh, parent present — keep
	})
	if _, err := spawn.Update(func(st *spawn.Store) error {
		return st.Set(spawn.Grant{Island: "alpha", MaxConcurrent: 5, TTL: time.Hour})
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanSpawnReap(context.Background())

	ids := agentIDs(t, "alpha")
	if !ids["a1"] || !ids["a4"] {
		t.Errorf("kept the wrong agents: %v (want a1 + a4 present)", ids)
	}
	if ids["a2"] || ids["a3"] {
		t.Errorf("did not reap due agents: %v (a2 ttl, a3 parent-gone should be gone)", ids)
	}
}

func TestSpawnReaper_RevokeReapsAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeRuntime{status: runtime.StatusStopped}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := srv.Handler()

	saveProjectWithAgents(t, "alpha", []project.AgentSpec{
		{ID: "a1", Type: "claude-code"},
		{ID: "a2", Type: "claude-code", Ephemeral: true, SpawnedBy: "a1", CreatedAt: time.Now()},
		{ID: "a3", Type: "claude-code", Ephemeral: true, SpawnedBy: "a1", CreatedAt: time.Now()},
	})
	if _, err := spawn.Update(func(st *spawn.Store) error {
		return st.Set(spawn.Grant{Island: "alpha", MaxConcurrent: 5})
	}); err != nil {
		t.Fatal(err)
	}

	if rr := do(t, h, http.MethodDelete, "/v1/islands/alpha/spawn-grant", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: got %d", rr.Code)
	}
	ids := agentIDs(t, "alpha")
	if !ids["a1"] {
		t.Error("revoke must not reap the non-ephemeral root agent")
	}
	if ids["a2"] || ids["a3"] {
		t.Errorf("revoke must reap all ephemeral sub-agents, still present: %v", ids)
	}
}
