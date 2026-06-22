package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/ledger"
)

// These tests drive the real bubbletea TUI model through teatest — feeding key
// events into a running tea.Program and asserting on rendered frames + the final
// model state. Per the Lane 6 brief the model is driven DIRECTLY: no daemon. The
// model is seeded with island state, and its async fetch commands (which would
// dial a daemon) simply fail to a harmless errMsg, leaving the seeded state in
// place. Where an overlay needs server data (the audit pane) we feed the
// loaded-message into the program ourselves, exactly as the Update loop would.

// seededModel returns a TUI model pre-populated with islands and a non-zero
// terminal size so View() renders the dashboard immediately. The client points
// at a dead unix socket under a temp HOME, so the periodic fetch commands fail
// fast (errMsg) without overwriting the seeded list.
func seededModel(t *testing.T, islands ...api.IslandInfo) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_HOST", "") // don't inherit a real host from the dev/CI env
	t.Setenv("DEJIMA_TOKEN", "")
	c, err := api.NewUnixClient() // dials ~/.dejima/dejimad.sock, which doesn't exist
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	m := initialTUIModel(c)
	m.islands = sortIslands(islands)
	m.width, m.height = 120, 40
	return m
}

func island(name string, agents ...string) api.IslandInfo {
	isl := api.IslandInfo{Name: name, Container: "running"}
	for _, a := range agents {
		isl.Agents = append(isl.Agents, api.AgentInfo{ID: a, Type: "claude-code"})
	}
	if len(isl.Agents) == 0 {
		isl.Agents = []api.AgentInfo{{ID: "a1", Type: "claude-code"}}
	}
	return isl
}

// waitForAll blocks until a single output read contains EVERY wanted substring.
// teatest's WaitFor drains the program's output buffer into its own local copy,
// so consecutive WaitFor calls each see only bytes emitted since the previous
// one — a static screen would make a second single-string wait time out. Hence
// every assertion point checks all of its substrings in one WaitFor.
func waitForAll(t *testing.T, tm *teatest.TestModel, wants ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, w := range wants {
			if !bytes.Contains(b, []byte(w)) {
				return false
			}
		}
		return true
	}, teatest.WithCheckInterval(20*time.Millisecond), teatest.WithDuration(8*time.Second))
}

func runModel(t *testing.T, m tuiModel) *teatest.TestModel {
	t.Helper()
	return teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
}

// TestTUIIslandListRenders: the dashboard lists seeded islands and j navigation
// moves the selection cursor off the first row.
func TestTUIIslandListRenders(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha"), island("beta")))
	waitForAll(t, tm, "Islands", "alpha", "beta")

	tm.Send(key("j"))
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.selected == 0 {
		t.Errorf("expected the cursor to move off row 0 after j, selected=%d", final.selected)
	}
}

// TestTUIConfirmPopupRequiresName: pressing 'd' on an island opens the purge
// confirm pop-up, which requires typing the island NAME. A wrong name does not
// arm the action.
func TestTUIConfirmPopupRequiresName(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("d"))
	waitForAll(t, tm, "Confirm", "Type the island name to confirm")

	for _, ch := range "wrong" {
		tm.Send(key(string(ch)))
	}
	tm.Send(key("enter"))
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if _, armed := final.dirtyOps["alpha"]; armed {
		t.Errorf("a wrong name should not arm the purge; dirtyOps=%v", final.dirtyOps)
	}
	if final.confirm != nil {
		t.Errorf("confirm should be closed after enter")
	}
}

// TestTUIConfirmTypedNameArms: typing the exact island name arms the purge —
// the model picks up a "purging" dirty-op for that island.
// Driven directly (not via teatest) because once the purge is armed the model
// dispatches the op command, whose result (against no daemon) races back and
// clears the transient "purging" hint — so the state is asserted synchronously,
// before the async result lands.
func TestTUIConfirmTypedNameArms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_HOST", "")
	c, _ := api.NewUnixClient()
	m := initialTUIModel(c)
	m.islands = sortIslands([]api.IslandInfo{island("gamma")})
	m.width, m.height = 120, 40

	// d → purge confirm armed on "gamma".
	mm, _ := m.handleKey(key("d"))
	m = mm.(tuiModel)
	if m.confirm == nil || m.confirm.verb != "purge" {
		t.Fatalf("d should arm a purge confirm, got %+v", m.confirm)
	}
	// Type the exact island name, then enter → runConfirmed dispatches the purge.
	for _, ch := range "gamma" {
		mm, _ = m.handleKey(key(string(ch)))
		m = mm.(tuiModel)
	}
	if m.confirm.answer != "gamma" {
		t.Fatalf("typed answer = %q, want gamma", m.confirm.answer)
	}
	mm, cmd := m.handleKey(key("enter"))
	m = mm.(tuiModel)
	if m.confirm != nil {
		t.Errorf("confirm should close on enter")
	}
	if m.dirtyOps["gamma"] != "purging" {
		t.Errorf("a matching name should arm the purge; dirtyOps=%v", m.dirtyOps)
	}
	if cmd == nil {
		t.Errorf("expected a purge command to be dispatched")
	}
}

// TestTUIActionMenu: 'm' opens the per-row action menu listing lifecycle verbs;
// esc closes it.
func TestTUIActionMenu(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("m"))
	waitForAll(t, tm, "Hibernate", "Purge")

	tm.Send(key("esc"))
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.menu != nil {
		t.Errorf("action menu should be closed after esc")
	}
}

// TestTUIAuditPane: 'A' opens the audit pane; once the (fed) load message
// arrives it shows the chain-verification banner. esc closes it.
func TestTUIAuditPane(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("A"))
	// The real load (no daemon) fails fast → "error" banner; once that's settled
	// our fed success message lands last and renders the clean-chain banner.
	waitForAll(t, tm, "Audit ledger", "error")

	tm.Send(auditLoadedMsg{resp: &api.AuditResponse{
		Entries: []ledger.Entry{
			{Seq: 1, Type: "island.create", Island: "alpha", Actor: "owner"},
			{Seq: 2, Type: "link.grant", Island: "alpha", Actor: "owner"},
		},
		Total: 2, Returned: 2, Verified: true,
	}})
	waitForAll(t, tm, "hash chain intact", "link.grant")

	tm.Send(key("esc"))
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.audit != nil {
		t.Errorf("audit pane should be closed after esc")
	}
}

// TestTUIAuditPaneTamper: a failed chain verification renders the tamper banner.
func TestTUIAuditPaneTamper(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")
	tm.Send(key("A"))
	waitForAll(t, tm, "Audit ledger", "error") // let the real (no-daemon) load settle first
	tm.Send(auditLoadedMsg{resp: &api.AuditResponse{
		Entries:  []ledger.Entry{{Seq: 1, Type: "island.create"}},
		Total:    1,
		Returned: 1,
		Verified: false,
		Error:    "seq 1 hash mismatch",
	}})
	waitForAll(t, tm, "CHAIN TAMPERED")
	tm.Send(key("esc")) // close the audit pane (in the pane, q would also close, not quit)
	tm.Send(key("q"))
	tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second))
}

// TestTUIHelpOverlay: '?' opens the help overlay; esc closes it.
func TestTUIHelpOverlay(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("?"))
	waitForAll(t, tm, "how to use it") // help overlay title

	tm.Send(key("esc")) // close help (in help mode q would also close, not quit)
	tm.Send(key("q"))   // now quit the dashboard
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.help {
		t.Errorf("help should be closed after esc")
	}
}

// TestTUIRemoveAgentRequiresID: on a multi-agent island, selecting an agent row
// and pressing X opens a confirm requiring the agent ID. Esc cancels it.
func TestTUIRemoveAgentRequiresID(t *testing.T) {
	tm := runModel(t, seededModel(t, island("multi", "a1", "a2")))
	waitForAll(t, tm, "multi")

	tm.Send(key("j")) // first agent row (multi-agent islands default expanded)
	tm.Send(key("X"))
	waitForAll(t, tm, "Confirm", "agent")

	tm.Send(key("esc"))
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.confirm != nil {
		t.Errorf("remove-agent confirm should be closed after esc")
	}
}

// TestTUIAgentMenuDrillIn: right reveals an island's agents (drill-in) and left
// collapses them — a core navigation transition.
func TestTUIAgentMenuDrillIn(t *testing.T) {
	m := seededModel(t, island("solo", "a1"))
	m.expanded["solo"] = false // start collapsed so we can assert the reveal
	tm := runModel(t, m)
	waitForAll(t, tm, "solo")

	tm.Send(key("right")) // expand → the "+ add agent" affordance becomes visible
	waitForAll(t, tm, "add agent")

	tm.Send(key("left")) // collapse again
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.islandExpandedByName("solo") {
		t.Errorf("island should be collapsed after left")
	}
}
