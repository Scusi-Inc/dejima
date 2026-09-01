package main

import (
	"os"
	"testing"
)

// A test binary must never open a window on the operator's screen.
//
// Caught in the act, not theorised: three stray windows sitting in another
// agent's tmux session, each running the TEST BINARY —
//
//	agent-d3:2  github-connect  (dejima.test)
//	agent-d3:3  github-connect  (dejima.test)
//	agent-d3:4  github-connect  (dejima.test)
//
// TMUX is always set inside an agent's pane, so canOpenNewWindow returned true
// under `go test` and any test reaching an opener ran a real `tmux new-window`
// against a live session. The operator had been reporting "a script took over my
// terminal" for a week. This was one of the scripts.
//
// Individual tests stubbing it false was the fragile half of the arrangement: it
// relied on every test knowing it was about to open a window, and the ones that
// got there through a key handler did not.
func TestTestBinaryNeverOpensAWindow(t *testing.T) {
	// TMUX set is the condition that made this fire in the field, so set it
	// explicitly rather than depending on the ambient environment — the guard
	// must hold on a developer's laptop too, where TMUX may be absent and the
	// test would otherwise pass for the wrong reason.
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")

	if canOpenNewWindow() {
		t.Error("canOpenNewWindow() is true inside a test binary — any test that " +
			"reaches an opener will run `tmux new-window` against whatever session " +
			"the operator is attached to")
	}
	// And the same on the platforms that do not need TMUX at all.
	_ = os.Unsetenv("TMUX")
	if canOpenNewWindow() {
		t.Error("canOpenNewWindow() is true inside a test binary on this platform")
	}
}
