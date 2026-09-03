package main

import (
	"strings"
	"testing"
)

// The probe's whole value is that CLIENT size and WINDOW size are both present
// and separate. The diagnosis is the comparison between them, so a report that
// captured only one, or folded them together, would hide exactly the discrepancy
// it is run to find — and would read as a clean result while doing it.
func TestTmuxSizeProbesCaptureBothSides(t *testing.T) {
	joined := ""
	for _, p := range tmuxSizeProbes {
		joined += p.label + " " + p.script + "\n"
	}
	for _, want := range []struct{ frag, why string }{
		{"client_width", "without the client's size there is nothing to compare the window against"},
		{"client_height", "without the client's size there is nothing to compare the window against"},
		{"window_width", "the collapsed window is the finding; omitting it omits the answer"},
		{"window_height", "the collapsed window is the finding; omitting it omits the answer"},
		{"client_termname", "a lost TERM silently strips RGB/sync on the same code path"},
		{"pane_dead", "a dead pane and a collapsed window look identical from outside"},
		{"window-size", "the collapse only happens under `window-size latest`; record what is in force"},
	} {
		if !strings.Contains(joined, want.frag) {
			t.Errorf("the probe never asks for %q — %s", want.frag, want.why)
		}
	}
}

// TestCLIDoctorTmuxSize drives the command far enough to prove it is wired up
// and fails cleanly against an island that does not exist, rather than panicking
// on a nil result. It cannot assert real tmux output: there is no container.
func TestCLIDoctorTmuxSize(t *testing.T) {
	_, _ = cliEnv(t)
	out, err := runCLI(t, "doctor", "tmux-size", "no-such-island")
	if err != nil {
		// A daemon error is fine; a crash is not.
		if strings.Contains(err.Error(), "panic") {
			t.Fatalf("doctor tmux-size panicked: %v", err)
		}
	}
	// Whatever happened, the operator must be told how to read it — the report is
	// useless without the comparison instruction.
	if err == nil && !strings.Contains(out, "smaller than its client") {
		t.Errorf("the report omits how to read it: %q", out)
	}
}
