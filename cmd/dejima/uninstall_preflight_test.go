//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// The uninstall hang was a mutual wait: launchctl bootout blocks until launchd
// has torn the job down, which includes the shell running the uninstall when
// that shell descends from the daemon. The preflight has to catch it BEFORE the
// sudo prompt and before islands are deleted, because that is where it wedged.
func TestPreflightAllowsNormalShell(t *testing.T) {
	// The test binary is a child of `go test`, not of dejimad.
	if _, inside := insideDaemonJob(); inside {
		t.Skip("this test process really does descend from dejimad")
	}
	if err := preflightNotInsideDaemon(false); err != nil {
		t.Errorf("preflight blocked a normal shell: %v", err)
	}
}

// --force must remain an escape hatch: the ancestry walk can misread unusual
// process trees, and an operator must never be locked out of uninstalling.
func TestPreflightForceAlwaysProceeds(t *testing.T) {
	if err := preflightNotInsideDaemon(true); err != nil {
		t.Errorf("--force should never block: %v", err)
	}
}

// parentOf walks real processes; it must return this process's actual parent
// rather than erroring, since the whole guard rests on it.
func TestParentOfResolvesRealAncestry(t *testing.T) {
	ppid, name, ok := parentOf(os.Getpid())
	if !ok {
		t.Skip("ps unavailable in this environment")
	}
	if ppid <= 0 {
		t.Errorf("parentOf(self) ppid = %d, want a real parent", ppid)
	}
	if name == "" {
		t.Error("parent name empty — the ancestry walk cannot match on it")
	}
	if strings.Contains(name, "/") {
		t.Errorf("process name should be a bare name, got %q", name)
	}
}
