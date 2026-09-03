package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
)

// Switching connection targets used to leave the PREVIOUS server's islands on
// screen: an in-flight request (8s timeout, 2s poll) could land after the switch
// and be applied as if fresh. Worse than cosmetic — the listMsg handler also
// clears lastError and daemonHelp, so a stale reply erased the diagnosis
// explaining why the new target was unreachable, and with it the offer to fix
// it. Observed live: header reading "local" above a full list of the remote
// server's islands.
func TestStaleFetchesAreDiscarded(t *testing.T) {
	base := func() tuiModel {
		m := initialTUIModel(nil)
		m.width, m.height = 100, 40
		m.gen = 3 // we have switched targets a few times
		m.lastError = "daemon unreachable"
		d := daemonDiagnosis{Cause: "Windows can't run the Dejima daemon"}
		m.daemonHelp = &d
		return m
	}

	t.Run("a reply from the previous target changes nothing", func(t *testing.T) {
		out, _ := base().Update(listMsg{gen: 2, islands: []api.IslandInfo{{Name: "ghost"}}})
		got := out.(tuiModel)
		if len(got.islands) != 0 {
			t.Errorf("stale islands were applied: %+v", got.islands)
		}
		if got.lastError == "" {
			t.Error("stale reply cleared lastError — the error is still true")
		}
		if got.daemonHelp == nil {
			t.Error("stale reply wiped the diagnosis, which also hides the [w] offer")
		}
	})

	t.Run("a reply from the current target applies normally", func(t *testing.T) {
		out, _ := base().Update(listMsg{gen: 3, islands: []api.IslandInfo{{Name: "real"}}})
		got := out.(tuiModel)
		if len(got.islands) != 1 || got.islands[0].Name != "real" {
			t.Fatalf("current-generation islands should apply, got %+v", got.islands)
		}
		if got.lastError != "" || got.daemonHelp != nil {
			t.Error("a successful load must clear the error and the diagnosis")
		}
	})

	t.Run("overview and detail are guarded too", func(t *testing.T) {
		out, _ := base().Update(overviewMsg{gen: 2, ov: &api.OverviewResponse{Owner: "someone-else", Role: "owner"}})
		if got := out.(tuiModel); got.callerOwner == "someone-else" {
			t.Error("stale overview set caller identity from the previous server")
		}
		out, _ = base().Update(detailMsg{gen: 2, info: &api.IslandInfo{Name: "ghost"}})
		if got := out.(tuiModel); got.detail != nil {
			t.Errorf("stale detail was applied: %+v", got.detail)
		}
	})
}

// The guard is only as good as the bump: if switching a target doesn't advance
// the generation, every message stays "current" and the discard never fires.
func TestSwitchingTargetsAdvancesGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	m.gen = 7
	m.activeHost = "10.0.0.1:7273" // currently pointed at a remote server
	m.switcher = &switcherModel{
		profiles: []clientcfg.Profile{{Name: "local", Host: ""}},
		cursor:   0,
	}

	out, _ := m.switcherActivate()
	got := out.(tuiModel)
	if got.gen != 8 {
		t.Fatalf("gen = %d, want 8 — switching targets must start a new generation", got.gen)
	}
	if got.activeHost != "" {
		t.Errorf("activeHost = %q, want the local socket", got.activeHost)
	}

	// The point of the bump, end to end: a reply already in flight against the
	// old remote when the switch happened is now discarded rather than painted.
	out, _ = got.Update(listMsg{gen: 7, islands: []api.IslandInfo{{Name: "from-the-old-server"}}})
	if final := out.(tuiModel); len(final.islands) != 0 {
		t.Errorf("in-flight reply from the previous target was applied: %+v", final.islands)
	}
}
