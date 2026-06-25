package main

import (
	"testing"

	"github.com/aoos/dejima/internal/link"
)

// TestDemoFleet: the synthetic fleet has a multi-agent flagship, a hibernated
// island, and a destructive pending action staged for the containment clip.
func TestDemoFleet(t *testing.T) {
	isls := demoIslands(0)
	if len(isls) < 3 {
		t.Fatalf("demo fleet should have several islands, got %d", len(isls))
	}
	var multi, hibernated bool
	for _, isl := range isls {
		if len(isl.Agents) >= 3 {
			multi = true
		}
		if isl.Container != "running" {
			hibernated = true
		}
	}
	if !multi {
		t.Error("demo fleet should include a multi-agent island (the hero shot)")
	}
	if !hibernated {
		t.Error("demo fleet should include a hibernated island for contrast")
	}

	// Overview totals track the fleet.
	o := demoOverview(0)
	if o.TotalIslands != len(isls) || o.Running == 0 || !o.DockerReachable {
		t.Errorf("demo overview mismatch: %+v vs %d islands", o, len(isls))
	}

	// A destructive pending action is staged (the B2 money shot).
	var destructive bool
	for _, a := range demoPending() {
		if a.Tier == link.TierDestructive {
			destructive = true
		}
	}
	if !destructive {
		t.Error("demo pending should include a destructive action for the containment clip")
	}
}

// TestDemoChurn: agent states cycle across ticks so the hero clip animates
// (working → needs-you → idle), not a frozen frame.
func TestDemoChurn(t *testing.T) {
	seen := map[string]bool{}
	for tick := 0; tick < 8; tick++ {
		seen[demoLatest(0, tick)] = true
	}
	if len(seen) < 3 {
		t.Errorf("demoLatest should cycle through ≥3 states over ticks, saw %v", seen)
	}
}
