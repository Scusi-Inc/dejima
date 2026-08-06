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
	script := agentLaunchScript(supervised, false)
	if !strings.Contains(script, headlessRestartMarker("w2")) {
		t.Fatalf("supervisor script must log %q (what headlessRestartCount greps):\n%s",
			headlessRestartMarker("w2"), script)
	}
	if !strings.Contains(script, "while true") {
		t.Fatal("a Restart=true headless agent must run inside the supervisor loop")
	}

	// A non-supervised headless agent has no loop and no marker.
	oneShot := &project.AgentSpec{ID: "w3", Type: "openclaw", Restart: false}
	if s := agentLaunchScript(oneShot, false); strings.Contains(s, "while true") {
		t.Fatalf("Restart=false headless agent should not be supervised:\n%s", s)
	}
}

// TestAgentLaunchResume: an interactive claude-code agent launches with
// `claude --continue` when a graceful restart requests resume, and plain
// `claude` (cold start) otherwise.
func TestAgentLaunchResume(t *testing.T) {
	a := &project.AgentSpec{ID: "a1", Type: "claude-code"}
	if s := agentLaunchScript(a, false); !strings.Contains(s, "exec claude") || strings.Contains(s, "--continue") {
		t.Errorf("cold start should be plain `claude`; got %q", s)
	}
	if s := agentLaunchScript(a, true); !strings.Contains(s, "claude --continue") {
		t.Errorf("resume should launch `claude --continue`; got %q", s)
	}
}

// TestInteractiveAgentSourcesSecrets guards the secrets-injection fix: an
// interactive agent must source the secrets profile.d hook before exec so the
// island's secrets reach its environment (and its Bash tool's). Sourcing it
// directly — not via a full login shell — avoids /etc/profile resetting PATH and
// dropping the agent binary. A launch that skips this is the bug where an added
// secret never reaches the agent.
func TestInteractiveAgentSourcesSecrets(t *testing.T) {
	a := &project.AgentSpec{ID: "a1", Type: "claude-code"}
	s := agentLaunchScript(a, false)
	if !strings.Contains(s, "/etc/profile.d/10-dejima-secrets.sh") {
		t.Errorf("interactive agent must source the secrets hook so secrets load; got %q", s)
	}
	if strings.Contains(s, "bash -lc") || strings.Contains(s, "bash -l ") {
		t.Errorf("must NOT use a login shell (it resets PATH, dropping the agent binary); got %q", s)
	}
}
