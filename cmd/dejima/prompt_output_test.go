package main

import (
	"bytes"
	"strings"
	"testing"
)

// A prompt must go where the caller says, not to the process's stdout.
//
// This is the guard for the noise itself, not for the prompt logic: the failure
// it prevents is a test suite emitting interactive prompts onto a terminal
// somebody else is looking at.
func TestPromptsDoNotReachProcessStdout(t *testing.T) {
	var buf bytes.Buffer
	orig := promptOut
	promptOut = &buf
	t.Cleanup(func() { promptOut = orig })

	// stdinReader is at EOF under test, so this returns immediately; the point is
	// where the PROMPT went, not what was read.
	readSingleKey("go? [Y/n]: ")

	if !strings.Contains(buf.String(), "go? [Y/n]:") {
		t.Errorf("the prompt did not reach the injected writer (got %q) — if it is not "+
			"going here it is going to the process's stdout, which is the terminal of "+
			"whoever happens to be attached", buf.String())
	}
}
