package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestEnterOpensShellVsAgent locks in the new navigation: Enter on an island row
// opens the island's contained shell (connectShell), NOT an agent; Enter on an
// agent row attaches that agent. TMUX is cleared so activateRow takes the
// in-process (quit-to-attach) path deterministically rather than a new window.
func TestEnterOpensShellVsAgent(t *testing.T) {
	t.Setenv("TMUX", "")

	islands := []api.IslandInfo{{
		Name:      "myrepo",
		Container: "running",
		Agents:    []api.AgentInfo{{ID: "p1", Type: "claude-code", Attachable: true}},
	}}

	newModel := func(sel int) tuiModel {
		m := initialTUIModel(nil)
		m.width, m.height = 100, 30
		m.expanded["myrepo"] = true
		m.islands = islands
		m.selected = sel
		return m
	}

	// Row 0 = the island. Enter → contained shell, no agent attach.
	island := newModel(0).activateRowModel(t)
	if island.connectShell != "myrepo" {
		t.Errorf("Enter on island should set connectShell=myrepo, got %q", island.connectShell)
	}
	if island.connectTo != "" || island.connectAgent != "" {
		t.Errorf("Enter on island must not attach an agent (connectTo=%q agent=%q)", island.connectTo, island.connectAgent)
	}

	// Row 1 = the agent. Enter → attaches that agent, no shell.
	agent := newModel(1).activateRowModel(t)
	if agent.connectTo != "myrepo" || agent.connectAgent != "p1" {
		t.Errorf("Enter on agent should attach it (connectTo=%q agent=%q)", agent.connectTo, agent.connectAgent)
	}
	if agent.connectShell != "" {
		t.Errorf("Enter on agent must not open a shell (connectShell=%q)", agent.connectShell)
	}
}

// activateRowModel runs activateRow and returns the resulting model.
func (m tuiModel) activateRowModel(t *testing.T) tuiModel {
	t.Helper()
	out, _ := m.activateRow()
	return out.(tuiModel)
}
