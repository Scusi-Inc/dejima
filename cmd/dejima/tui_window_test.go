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

// openAgents assigned m.lastError inside its fan-out loop, so each failure
// overwrote the previous one: N broken windows reported a single message naming
// only the LAST agent, which reads as one unlucky agent rather than a systemic
// failure. That mattered most on the one path that opens many windows at once
// (Enter on an island row). The aggregate must name the count so the two cases
// are distinguishable, while a lone failure stays its own bare error.
func TestOpenAgentsError(t *testing.T) {
	t.Run("single failure reads as its own error", func(t *testing.T) {
		got := openAgentsError(3, []string{"a2"}, "open-in-new-window needs tmux")
		if got != "open-in-new-window needs tmux" {
			t.Errorf("single failure should not be decorated, got %q", got)
		}
	})

	t.Run("several failures report count, agents, and a cause", func(t *testing.T) {
		got := openAgentsError(5, []string{"a1", "a2", "a3"}, "wt.exe: not found")
		for _, want := range []string{"3 of 5", "a1, a2, a3", "wt.exe: not found"} {
			if !strings.Contains(got, want) {
				t.Errorf("aggregate %q missing %q", got, want)
			}
		}
	})

	t.Run("a total fan-out failure is distinguishable from one", func(t *testing.T) {
		all := openAgentsError(4, []string{"a1", "a2", "a3", "a4"}, "boom")
		one := openAgentsError(4, []string{"a4"}, "boom")
		if all == one {
			t.Fatal("4-of-4 failing must not render identically to 1 failing")
		}
		if !strings.Contains(all, "4 of 4") {
			t.Errorf("want the full-failure count surfaced, got %q", all)
		}
	})
}
