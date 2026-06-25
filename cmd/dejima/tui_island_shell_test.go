package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestEnterOpensIslandAgents locks in the navigation: Enter on an island opens
// its agents (each in a window); Enter on an agent opens just that one; and a
// non-GUI terminal — which can't spawn windows — falls back to the in-island
// shell. We drive canOpenNewWindow directly so the assertions hold on every GOOS
// and avoid actually exec-ing a terminal.
func TestEnterOpensIslandAgents(t *testing.T) {
	orig := canOpenNewWindow
	defer func() { canOpenNewWindow = orig }()

	mk := func(agents []api.AgentInfo, sel int, gui bool) tuiModel {
		canOpenNewWindow = func() bool { return gui }
		m := initialTUIModel(nil)
		m.width, m.height = 100, 30
		m.expanded["myrepo"] = true
		m.islands = []api.IslandInfo{{Name: "myrepo", Container: "running", Agents: agents}}
		m.selected = sel
		return m
	}
	oneAgent := []api.AgentInfo{{ID: "p1", Type: "claude-code", Attachable: true}}

	// Non-GUI terminal: Enter on the island falls back to the contained shell
	// (it can't open windows), and does NOT attach an agent.
	island := mk(oneAgent, 0, false).activateRowModel(t)
	if island.connectShell != "myrepo" {
		t.Errorf("non-GUI Enter on island should fall back to the shell, connectShell=%q", island.connectShell)
	}
	if island.connectTo != "" || island.connectAgent != "" {
		t.Errorf("Enter on island must not attach an agent (connectTo=%q agent=%q)", island.connectTo, island.connectAgent)
	}

	// Enter on the agent row attaches that one agent.
	agent := mk(oneAgent, 1, false).activateRowModel(t)
	if agent.connectTo != "myrepo" || agent.connectAgent != "p1" {
		t.Errorf("Enter on agent should attach it (connectTo=%q agent=%q)", agent.connectTo, agent.connectAgent)
	}
	if agent.connectShell != "" {
		t.Errorf("Enter on agent must not open a shell (connectShell=%q)", agent.connectShell)
	}

	// Many attachable agents in a GUI terminal → confirm before opening them all
	// (this path returns the confirm before opening, so it never exec's a window).
	many := make([]api.AgentInfo, 0, 5)
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5"} {
		many = append(many, api.AgentInfo{ID: id, Attachable: true})
	}
	out, _ := mk(many, 0, true).openIslandAgents("myrepo")
	if c := out.(tuiModel).confirm; c == nil || c.verb != "open-all-agents" || c.island != "myrepo" {
		t.Errorf("opening >%d agents should confirm first, got %+v", openAllConfirmThreshold, out.(tuiModel).confirm)
	}
}

// TestAttachableAgentIDs: only attachable (non-headless) agents are opened in
// bulk; headless agents are skipped (their logs open via Enter on their row).
func TestAttachableAgentIDs(t *testing.T) {
	m := initialTUIModel(nil)
	m.islands = []api.IslandInfo{{Name: "mix", Agents: []api.AgentInfo{
		{ID: "a1", Attachable: true},
		{ID: "h1", Attachable: false},
		{ID: "a2", Attachable: true},
	}}}
	got := m.attachableAgentIDs("mix")
	if len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Errorf("attachableAgentIDs = %v, want [a1 a2] (headless skipped)", got)
	}
}

// TestOpenIslandShellFallback: the `$` path (openIslandShell) attaches the
// contained shell in place when the terminal can't open a window.
func TestOpenIslandShellFallback(t *testing.T) {
	orig := canOpenNewWindow
	canOpenNewWindow = func() bool { return false }
	defer func() { canOpenNewWindow = orig }()

	m := initialTUIModel(nil)
	m.width, m.height = 100, 30
	out, _ := m.openIslandShell("myrepo")
	if out.(tuiModel).connectShell != "myrepo" {
		t.Errorf("$ should open the in-island shell (connectShell=myrepo), got %q", out.(tuiModel).connectShell)
	}
}

// activateRowModel runs activateRow and returns the resulting model.
func (m tuiModel) activateRowModel(t *testing.T) tuiModel {
	t.Helper()
	out, _ := m.activateRow()
	return out.(tuiModel)
}
