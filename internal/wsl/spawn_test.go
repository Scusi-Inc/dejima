package wsl

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// EVERY wsl.exe spawn must be console-isolated. One missed call reintroduces the
// bug: the console only has to be disturbed once for the keystrokes in flight to
// be mangled, and the dashboard polls on a tick, so a single un-isolated call
// site fires every few seconds while someone is typing.
//
// This reads the source because the property is "no call site was forgotten",
// which is about the set of call sites and cannot be observed from behaviour on
// a non-Windows machine. It is the same reason the materializer guard scans
// source: the defect is an omission, and an omission has no runtime signature
// until it bites.
func TestEveryWSLSpawnIsolatesTheConsole(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(".", "wsl.go"))
	if err != nil {
		t.Fatalf("read wsl.go: %v", err)
	}
	lines := strings.Split(string(body), "\n")

	spawn := regexp.MustCompile(`execCommand\("wsl\.exe"`)
	found := 0
	for i, ln := range lines {
		if !spawn.MatchString(ln) {
			continue
		}
		found++
		// The isolation call must come before the process starts. Look at the
		// next few lines rather than the whole function, so moving a call site
		// away from its guard fails here.
		window := strings.Join(lines[i:min(i+4, len(lines))], "\n")
		if !strings.Contains(window, "isolateConsole(cmd)") {
			t.Errorf("wsl.go:%d spawns wsl.exe without isolateConsole — that call will "+
				"share the operator's console and can mangle their keystrokes:\n%s", i+1, window)
		}
	}
	if found == 0 {
		// A guard that finds nothing must not report success. If the spawn
		// pattern is refactored, this test has to fail rather than quietly
		// vouch for a file it no longer understands.
		t.Fatal("found no wsl.exe spawn sites at all — this guard can no longer see what it checks")
	}
	t.Logf("checked %d wsl.exe spawn sites", found)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
