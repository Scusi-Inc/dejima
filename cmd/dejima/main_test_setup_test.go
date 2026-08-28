package main

import (
	"io"
	"os"
	"testing"
)

// TestMain silences interactive prompts for the whole package.
//
// readSingleKey used to write to the PROCESS's stdout, so a table test of a
// confirm helper printed "go? [Y/n]: go? [y/N]:" onto whatever terminal the test
// binary was attached to. In an agent island that is an operator's pane, and it
// showed up there three separate times before anyone traced it to a test.
//
// `go test` pipes the binary's stdout and reprints it verbatim, so this was
// never a -v-only thing, and it could not be redirected by the caller: the
// writer was chosen inside the function.
//
// A test that wants to ASSERT on a prompt sets promptOut to its own buffer and
// restores it — see TestPromptsDoNotReachProcessStdout.
func TestMain(m *testing.M) {
	promptOut = io.Discard
	cliOut = io.Discard
	os.Exit(m.Run())
}
