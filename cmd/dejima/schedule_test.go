package main

import "testing"

// The `dejima schedule list` / `dejima schedule rm` commands are wired under a
// `schedule` parent. Also satisfies the coverage gate for the two new commands.
func TestScheduleCommands(t *testing.T) {
	sched := newScheduleCmd()
	if sched.Name() != "schedule" {
		t.Fatalf("parent Name() = %q, want schedule", sched.Name())
	}
	sub := map[string]bool{}
	for _, c := range sched.Commands() {
		sub[c.Name()] = true
	}
	if !sub["list"] {
		t.Error("missing `schedule list` subcommand")
	}
	if !sub["rm"] {
		t.Error("missing `schedule rm` subcommand")
	}
}
