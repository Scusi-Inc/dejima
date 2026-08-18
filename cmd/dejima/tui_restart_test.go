package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
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

// busyIsland builds an island where one agent is mid-task: State "running" with
// no terminal latest event is what agentStatus reads as "working". Liveness only
// rides the detail endpoint, so this goes into m.detail, not m.islands — which is
// itself part of what's under test.
func busyIsland(t *testing.T, working string, agents ...string) tuiModel {
	t.Helper()
	isl := island("alpha", agents...)
	for i := range isl.Agents {
		if isl.Agents[i].ID == working {
			isl.Agents[i].State = "running"
			isl.Agents[i].AgentState = &api.AgentStateInfo{Latest: "tool-use"}
		} else {
			isl.Agents[i].State = "running"
			isl.Agents[i].AgentState = &api.AgentStateInfo{Latest: "waiting-for-input"}
		}
	}
	m := seededModel(t, isl)
	m.detail = &isl
	return m
}

// An operator adds a secret and reaches for "restart everything". If that one
// keystroke also restarts the agent that is three minutes into a task, applying
// a secret has silently cost them work. Mid-task agents are listed — hiding them
// would be its own lie — but not pre-selected, and the pane says why: an unticked
// box with no explanation reads as an oversight, and the fix for an oversight is
// to press [a].
func TestRestartChecklistLeavesMidTaskAgentsUnticked(t *testing.T) {
	m := busyIsland(t, "a2", "a1", "a2").openRestartView("alpha")
	if m.restartPane == nil || len(m.restartPane.items) != 2 {
		t.Fatalf("checklist should list both agents; got %+v", m.restartPane)
	}
	for _, it := range m.restartPane.items {
		wantSelected := it.id != "a2"
		if it.selected != wantSelected {
			t.Errorf("agent %s: selected=%v, want %v (a2 is mid-task)", it.id, it.selected, wantSelected)
		}
		if it.busy != (it.id == "a2") {
			t.Errorf("agent %s: busy=%v, want %v", it.id, it.busy, it.id == "a2")
		}
	}
	out := m.restartPane.view(100)
	if !strings.Contains(out, "working") || !strings.Contains(out, "unticked") {
		t.Errorf("pane must show the busy marker AND explain the empty box; got %q", out)
	}
}

// The busy signal only exists on the detail endpoint. If the list copy is used,
// every agent reads as idle and the guard above quietly stops applying — the
// failure mode being that it still looks like it works.
func TestRestartChecklistPrefersDetailForLiveness(t *testing.T) {
	m := busyIsland(t, "a2", "a1", "a2")
	// The list copy has no liveness at all — this is what the dashboard holds.
	m.islands = sortIslands([]api.IslandInfo{island("alpha", "a1", "a2")})
	pane := m.openRestartView("alpha").restartPane
	for _, it := range pane.items {
		if it.id == "a2" && !it.busy {
			t.Error("mid-task agent read as idle — openRestartView fell back to the list, which never carries AgentState")
		}
	}
}

// The fix that would have prevented the incident: restart has to be FINDABLE.
// Before this, the only restart affordance in the whole TUI was [R] inside the
// secrets pane — a key that means "refresh" everywhere else and sits one shift
// from [r] erase. An operator looking in the obvious place, the agent's [s]
// menu, found no restart and a destructive near-homonym one level up.
func TestAgentSettingsMenuOffersRestart(t *testing.T) {
	m := seededModel(t, island("alpha", "a1", "a2"))
	m.expanded["alpha"] = true
	mm, ok := m.buildMenuFor(treeRow{kind: rowAgent, island: "alpha", agentID: "a1"})
	if !ok {
		t.Fatal("an agent row should have a settings menu")
	}
	var warm, cold *actionMenuItem
	for i, it := range mm.menu.items {
		switch {
		case strings.HasPrefix(it.label, "Restart ("):
			warm = &mm.menu.items[i]
		case strings.HasPrefix(it.label, "Restart cold"):
			cold = &mm.menu.items[i]
		}
	}
	if warm == nil || cold == nil {
		t.Fatalf("agent menu should offer both restart variants; items=%+v", mm.menu.items)
	}
	// Resume is the default because it is what people mean by "restart it"; the
	// label has to say so, or the two lines are indistinguishable under pressure.
	if !strings.Contains(warm.label, "conversation") || !strings.Contains(cold.label, "new conversation") {
		t.Errorf("restart labels must say what happens to the conversation; got %q / %q", warm.label, cold.label)
	}
	res, _ := mm.chooseMenuItem(*warm)
	got := res.(tuiModel)
	if got.confirm == nil || got.confirm.verb != "restart-agent" || got.confirm.agent != "a1" {
		t.Fatalf("choosing Restart should arm restart-agent for a1; got %+v", got.confirm)
	}
	if got.confirm.strict {
		t.Error("an idle agent's restart should be a plain y/n — reserve the typed gate for mid-task")
	}
}

// Restarting an agent that is working throws away the turn it's in. Same verb,
// higher price, so it costs a typed id rather than a keystroke — and the prompt
// has to say what's being spent.
func TestMidTaskRestartEscalatesTheGate(t *testing.T) {
	m := busyIsland(t, "a2", "a1", "a2")
	mm, ok := m.buildMenuFor(treeRow{kind: rowAgent, island: "alpha", agentID: "a2"})
	if !ok {
		t.Fatal("an agent row should have a settings menu")
	}
	var warm actionMenuItem
	for _, it := range mm.menu.items {
		if strings.HasPrefix(it.label, "Restart (") {
			warm = it
		}
	}
	res, _ := mm.chooseMenuItem(warm)
	got := res.(tuiModel)
	if got.confirm == nil || !got.confirm.strict {
		t.Fatalf("restarting a mid-task agent should escalate the gate; got %+v", got.confirm)
	}
	prompt := got.renderConfirm()
	if !strings.Contains(prompt, "working right now") || !strings.Contains(prompt, "the agent id") {
		t.Errorf("mid-task restart confirm must name the cost and the typed gate; got %q", prompt)
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
