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
	// `&&` would skip the pause exactly then.
	if i := strings.Index(inner, "pause"); i > 0 {
		before := inner[:i]
		if strings.HasSuffix(strings.TrimSpace(before), "&&") {
			t.Error("the pause is chained with && so it is skipped when the command fails — " +
				"which is the case that most needs reading")
		}
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
