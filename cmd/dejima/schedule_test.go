package main

import "testing"

// The schedule verbs are wired under a `schedule` parent AND reachable from the
// binary — the second half is the one a missing AddCommand breaks.
func TestScheduleCommands(t *testing.T) {
	if name := newScheduleCmd().Name(); name != "schedule" {
		t.Fatalf("parent Name() = %q, want schedule", name)
	}
	requirePaths(t, rootCommandPaths(t),
		"dejima schedule list",
		"dejima schedule rm",
	)
}
