package api

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// TestSupervisorMarkerContract guards the coupling between the supervisor loop
// (which logs the restart marker) and headlessRestartCount (which greps for it):
// if the marker text drifts on one side, the TUI restart count silently breaks.
func TestSupervisorMarkerContract(t *testing.T) {
	supervised := &project.AgentSpec{ID: "w2", Type: "openclaw", Restart: true}
	script := agentLaunchScript(supervised)
	if !strings.Contains(script, headlessRestartMarker("w2")) {
		t.Fatalf("supervisor script must log %q (what headlessRestartCount greps):\n%s",
			headlessRestartMarker("w2"), script)
	}
	if !strings.Contains(script, "while true") {
		t.Fatal("a Restart=true headless agent must run inside the supervisor loop")
	}

	// A non-supervised headless agent has no loop and no marker.
	oneShot := &project.AgentSpec{ID: "w3", Type: "openclaw", Restart: false}
	if s := agentLaunchScript(oneShot); strings.Contains(s, "while true") {
		t.Fatalf("Restart=false headless agent should not be supervised:\n%s", s)
	}
}
