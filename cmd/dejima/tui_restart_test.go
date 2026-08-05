package main

import (
	"strings"
	"testing"
)

// TestRestartChecklist drives the "which agents to restart" pane: it lists the
// island's agents (all pre-selected), Enter with selections starts a restart,
// [a] toggles all off (Enter then refuses), and [!] hands off to a recreate.
func TestRestartChecklist(t *testing.T) {
	base := seededModel(t, island("alpha", "a1", "a2"))

	m := base.openRestartView("alpha")
	if m.restartPane == nil || len(m.restartPane.items) != 2 {
		t.Fatalf("checklist should list the island's 2 agents; got %+v", m.restartPane)
	}

	// All selected by default → Enter fires a restart (busy + a command).
	mm, cmd := m.restartKey(key("enter"))
	m = mm.(tuiModel)
	if !m.restartPane.busy || cmd == nil {
		t.Fatalf("Enter with selections should start a restart; busy=%v cmd=%v", m.restartPane.busy, cmd)
	}

	// [a] toggles everything off; Enter then errors rather than restarting nothing.
	m = base.openRestartView("alpha")
	mm, _ = m.restartKey(key("a"))
	m = mm.(tuiModel)
	mm, cmd = m.restartKey(key("enter"))
	m = mm.(tuiModel)
	if cmd != nil || m.restartPane.err == "" {
		t.Errorf("Enter with nothing selected should error; err=%q cmd=%v", m.restartPane.err, cmd)
	}

	// [!] closes the checklist and arms the recreate-island confirm (first-secret case).
	m = base.openRestartView("alpha")
	mm, _ = m.restartKey(key("!"))
	m = mm.(tuiModel)
	if m.restartPane != nil || m.confirm == nil || m.confirm.verb != "recreate-island" {
		t.Errorf("[!] should arm recreate-island; confirm=%+v pane=%+v", m.confirm, m.restartPane)
	}
}

// TestAgentRestartCommand covers the CLI verb `dejima agent restart <island>
// <agent-id> [--resume]` (relaunches an agent in place to load a new secret).
func TestAgentRestartCommand(t *testing.T) {
	cmd := newAgentRestartCmd()
	if !strings.HasPrefix(cmd.Use, "restart") {
		t.Fatalf("restart command Use = %q, want it to start with \"restart\"", cmd.Use)
	}
	if cmd.Flags().Lookup("resume") == nil {
		t.Errorf("`dejima agent restart` should have a --resume flag")
	}
}
