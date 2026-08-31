package main

import "testing"

// A phase that ran but did not achieve its goal must not be recorded as done.
// The wizard had only two outcomes — done, or abort with an error — and an
// OPTIONAL phase is neither: local models returned nil after a failed install so
// the wizard would carry on, and the runner then marked it complete. The
// operator's own fix for a broken install (uninstall, reinstall) therefore
// skipped the exact phase they were reinstalling for, silently.
func TestIncompletePhaseIsNotRecordedAsDone(t *testing.T) {
	pc := &provCtx{state: &provState{}}
	pc.markIncomplete("local-models")

	// This mirrors the runner's decision after a phase returns nil.
	if !pc.incomplete["local-models"] {
		t.Fatal("markIncomplete did not record the phase")
	}
	if pc.incomplete["docker"] {
		t.Error("marking one phase incomplete must not affect another")
	}
	if pc.state.done("local-models") {
		t.Error("an incomplete phase must not read as done")
	}
}

// The inverse: a phase that succeeded is recorded, or the wizard re-runs
// finished work on every invocation.
func TestCompletedPhaseStillRecords(t *testing.T) {
	pc := &provCtx{state: &provState{}}
	if pc.incomplete["docker"] {
		t.Fatal("no phase should start out incomplete")
	}
	pc.state.markDone("docker")
	if !pc.state.done("docker") {
		t.Error("a finished phase must resume as done")
	}
}
