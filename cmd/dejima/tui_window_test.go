package main

import (
	"strings"
	"testing"
)

// `dejima agent open` takes the agent id POSITIONALLY; connect/logs take it as
// --agent. The Windows tab builder must honor that per verb — passing --agent to
// `agent open` is the "unknown flag: --agent" that broke opening OpenClaw's
// console on Windows.
func TestWindowsRunCommandAgentPlacement(t *testing.T) {
	got := windowsRunCommand("dejima.exe", "agent open", "home", "o1", nil)
	if strings.Contains(got, "--agent") {
		t.Errorf("agent open must pass the id positionally, not --agent: %q", got)
	}
	if !strings.Contains(got, "agent open home o1") {
		t.Errorf("expected positional `agent open home o1`, got %q", got)
	}

	got = windowsRunCommand("dejima.exe", "connect", "home", "o1", nil)
	if !strings.Contains(got, "connect home --agent o1") {
		t.Errorf("connect should use --agent: %q", got)
	}

	// No agent id → no trailing agent token either way.
	got = windowsRunCommand("dejima.exe", "agent open", "home", "", nil)
	if strings.Contains(got, "--agent") || strings.HasSuffix(strings.TrimSpace(got), "home o1") {
		t.Errorf("no agent id → bare `agent open home`: %q", got)
	}
}
