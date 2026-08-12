package main

import "testing"

// runSessionLoop does `restoreConsole, _ := prepareConsoleOutput()` followed by
// an unconditional `defer restoreConsole()`, discarding the error — so a nil func
// on any return path would panic on detach rather than at attach, where it would
// have been noticed. Both builds must hand back something callable on EVERY path,
// including the Windows failure paths (which is what `go test` exercises there:
// stdout is a pipe, so GetConsoleMode fails and the error branch is the one taken).
func TestPrepareConsoleOutputAlwaysReturnsCallableRestore(t *testing.T) {
	restore, err := prepareConsoleOutput()
	if restore == nil {
		t.Fatalf("restore func is nil (err=%v); the attach path defers it unconditionally", err)
	}
	// An error is expected and fine under test — there is no console to tune. The
	// contract being guarded is only that restoring is always safe to call.
	restore()
}
