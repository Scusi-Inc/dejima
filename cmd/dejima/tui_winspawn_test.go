package main

import (
	"strings"
	"testing"
)

// `cmd /c` closes the window the instant its command returns, so anything that
// finishes — successfully or not — vanishes before it can be read. The operator
// hit this twice: `github connect` "pulled up a terminal briefly then snapped
// back", and the gateway UI did the same. Both were reported by the TUI as
// opened, which they were; what they were not was READABLE.
//
// This asserts on the command string rather than by spawning a window, because
// the property is what cmd.exe is told to do and there is no cmd.exe here.
func TestWindowsSpawnKeepsTheWindowOpen(t *testing.T) {
	inner := windowsInnerCommand("dejima.exe", "agent open", "isl", "a1", nil, "wsl://dejima", "ui-isl")

	if !strings.Contains(inner, "pause") {
		t.Errorf("nothing keeps the window open; the output vanishes with the process:\n%s", inner)
	}
	// `&` and not `&&` — a FAILING command is the case most worth reading, and
	// `&&` would skip everything after it exactly then.
	//
	// The check is on what follows THE COMMAND, not on what precedes "pause".
	// The first version tested the latter and a mutation chaining with && sailed
	// through it: the separator it changed was three tokens earlier.
	cmdEnd := strings.Index(inner, "agent open")
	if cmdEnd < 0 {
		t.Fatalf("cannot locate the command in the line; this check has no subject:\n%s", inner)
	}
	tail := inner[cmdEnd:]
	if strings.Contains(tail, "&&") {
		t.Errorf("the command is chained with && so a FAILURE skips the pause — which is the "+
			"case that most needs reading:\n%s", inner)
	}
	// The command itself must still be there and still be first.
	if !strings.Contains(inner, "agent open") {
		t.Errorf("the command being run went missing:\n%s", inner)
	}
	if strings.Index(inner, "pause") < strings.Index(inner, "agent open") {
		t.Errorf("pauses before running anything:\n%s", inner)
	}
}

// The environment the spawned window needs must survive the change.
func TestWindowsSpawnCarriesHostAndTitle(t *testing.T) {
	inner := windowsInnerCommand("dejima.exe", "term attach", "h1", "", nil, "wsl://dejima", "host/h1")
	for _, want := range []string{"DEJIMA_HOST=wsl://dejima", "DEJIMA_TAB_TITLE=host/h1"} {
		if !strings.Contains(inner, want) {
			t.Errorf("lost %q from the spawned environment:\n%s", want, inner)
		}
	}
}

// cmd.exe has no sane quoting, so the metacharacter refusal is a safety
// property and must not be weakened by anything above.
func TestWindowsSpawnStillRefusesMetacharacters(t *testing.T) {
	if err := openWindowsTerminal("dejima.exe", "agent open", `isl&calc`, "", "t", nil, "h"); err == nil {
		t.Error("accepted an island name containing a cmd.exe metacharacter")
	}
}
