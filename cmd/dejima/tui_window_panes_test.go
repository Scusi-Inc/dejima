package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Opening an island opens everything in it. As tabs that buried the dashboard
// under several lookalike titles, so inside tmux the agents tile into ONE
// window as panes. This asserts the shape of what tmux is actually told,
// because the failure mode is subtle: a split that targets the active pane
// instead of the window halves an already-half pane, and panes without titles
// give no way to tell which agent is which once the per-tab title is gone.
func TestOpenAgentPanes_TilesOneWindow(t *testing.T) {
	var calls [][]string
	tmuxDriveAllowed = func() bool { return true }
	tmuxCmd = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case len(args) > 0 && args[0] == "new-window":
			return exec.Command("printf", "@7 %%3")
		case len(args) > 0 && args[0] == "split-window":
			return exec.Command("printf", "%%9")
		}
		return exec.Command("true")
	}
	t.Cleanup(func() {
		tmuxDriveAllowed = func() bool { return !testing.Testing() }
		tmuxCmd = exec.Command
	})

	m := tuiModel{}
	if err := m.openAgentPanes("isl", []string{"a1", "a2", "a3"}); err != nil {
		t.Fatalf("openAgentPanes: %v", err)
	}

	var newWindows, splits, layouts, titles int
	for _, c := range calls {
		switch c[1] {
		case "new-window":
			newWindows++
		case "split-window":
			splits++
			// Every split must target the WINDOW. Targeting the active pane is
			// how a third agent ends up in a sliver.
			if idx := argIndex(c, "-t"); idx < 0 || c[idx+1] != "@7" {
				t.Errorf("split did not target the window: %v", c)
			}
		case "select-layout":
			layouts++
		case "select-pane":
			titles++
		}
	}
	if newWindows != 1 {
		t.Errorf("new-window called %d times, want exactly 1 — the point is one window", newWindows)
	}
	if splits != 2 {
		t.Errorf("split-window called %d times, want 2 for 3 agents", splits)
	}
	if layouts == 0 {
		t.Error("never re-tiled; tmux splits the active pane, so panes end up uneven")
	}
	if titles != 3 {
		t.Errorf("set %d pane titles, want 3 — one tab means one OSC title, so the pane "+
			"borders are the only thing naming each agent", titles)
	}

	// Each agent must actually be launched, once.
	joined := ""
	for _, c := range calls {
		joined += strings.Join(c, " ") + "\n"
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if n := strings.Count(joined, "--agent '"+id+"'"); n != 1 {
			t.Errorf("agent %s launched %d times, want 1", id, n)
		}
	}
}

// A test binary must not reach a real tmux session. This is the guard that was
// missing when opener tests put stray windows in an operator's session.
func TestOpenAgentPanes_RefusesUnderTest(t *testing.T) {
	m := tuiModel{}
	if err := m.openAgentPanes("isl", []string{"a1", "a2"}); err == nil {
		t.Fatal("openAgentPanes ran from a test binary without being opted in")
	}
}

func argIndex(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
