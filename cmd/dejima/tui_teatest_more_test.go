package main

// Further teatest coverage of the TUI: the confirm pop-ups for every lifecycle
// verb (reset / upgrade / recreate / rename / relabel / force-purge / setup-ssh
// / update), the full action menu, and the manual-name new-tab labelling. These
// extend tui_teatest_test.go (which owns the harness: seededModel, runModel,
// waitForAll, key, island) and drive the §17 coverage-matrix rows to A.

import (
	"errors"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// driveKeys applies a sequence of key presses to a model synchronously (no
// teatest), returning the resulting model — handy for asserting the confirm
// state a key arms before any async command result can race back.
func driveKeys(t *testing.T, m tuiModel, keys ...string) tuiModel {
	t.Helper()
	for _, k := range keys {
		mm, _ := m.handleKey(key(k))
		m = mm.(tuiModel)
	}
	return m
}

// TestTUIResetConfirm: 'r' on an island arms a reset confirm (y/Enter to apply,
// workspace preserved).
func TestTUIResetConfirm(t *testing.T) {
	m := driveKeys(t, seededModel(t, island("alpha")), "r")
	if m.confirm == nil || m.confirm.verb != "reset" || m.confirm.island != "alpha" {
		t.Fatalf("r should arm a reset confirm on alpha, got %+v", m.confirm)
	}
	if !strings.Contains(m.renderConfirm(), "Clear agent state") {
		t.Errorf("reset confirm prompt: %q", m.renderConfirm())
	}
}

// TestTUIUpgradeViaActionMenu: island upgrade (recreate-on-image) is no longer a
// top-level key — it moved into the [m] actions menu. Selecting it arms the
// upgrade confirm.
func TestTUIUpgradeViaActionMenu(t *testing.T) {
	m := seededModel(t, island("alpha"))
	mm, ok := m.openActionMenu()
	if !ok {
		t.Fatal("island row should have an action menu")
	}
	// Find the Upgrade item and select it.
	found := false
	for i, it := range mm.menu.items {
		if strings.HasPrefix(it.label, "Upgrade") {
			mm.menu.sel = i
			found = true
			break
		}
	}
	if !found {
		t.Fatal("action menu should list an Upgrade item")
	}
	res, _ := mm.actionMenuKey(key("enter"))
	m = res.(tuiModel)
	if m.confirm == nil || m.confirm.verb != "upgrade" || m.confirm.island != "alpha" {
		t.Fatalf("selecting Upgrade should arm an upgrade confirm on alpha, got %+v", m.confirm)
	}
	if !strings.Contains(m.renderConfirm(), "current island image") {
		t.Errorf("upgrade confirm prompt: %q", m.renderConfirm())
	}
}

// TestTUIRenameIslandConfirm: 'e' on an island row arms a rename (cosmetic
// title) confirm, pre-filled with the current title.
func TestTUIRenameIslandConfirm(t *testing.T) {
	isl := island("alpha")
	isl.Title = "Alpha Project"
	m := driveKeys(t, seededModel(t, isl), "e")
	if m.confirm == nil || m.confirm.verb != "rename-island" {
		t.Fatalf("e on an island row should arm rename-island, got %+v", m.confirm)
	}
	if m.confirm.answer != "Alpha Project" {
		t.Errorf("rename confirm should pre-fill the current title, got %q", m.confirm.answer)
	}
	if !strings.Contains(m.renderConfirm(), "display title") {
		t.Errorf("rename-island prompt: %q", m.renderConfirm())
	}
}

// TestTUIRelabelAgentConfirm: 'e' on an agent row arms a relabel-agent confirm.
func TestTUIRelabelAgentConfirm(t *testing.T) {
	m := seededModel(t, island("multi", "a1", "a2"))
	m.expanded["multi"] = true
	// j moves onto the first agent row (multi-agent islands render expanded).
	m = driveKeys(t, m, "j", "e")
	if m.confirm == nil || m.confirm.verb != "relabel-agent" {
		t.Fatalf("e on an agent row should arm relabel-agent, got %+v", m.confirm)
	}
	if !strings.Contains(m.renderConfirm(), "Rename agent") {
		t.Errorf("relabel-agent prompt: %q", m.renderConfirm())
	}
}

// TestTUIForcePurgeOnUnpushedWork: a purge blocked by the unpushed-work guard
// (the daemon returns an error mentioning --force) transitions into a
// force-purge confirm whose prompt names the lost work — the §17 "confirm covers
// uncommitted/unpushed warning in the same pop-up" row.
func TestTUIForcePurgeOnUnpushedWork(t *testing.T) {
	m := seededModel(t, island("alpha"))
	guard := errors.New("island has unpushed commits; re-run with --force to purge anyway")
	mm, _ := m.Update(opCompleteMsg{name: "alpha", verb: "purge", err: guard})
	m = mm.(tuiModel)
	if m.confirm == nil || m.confirm.verb != "force-purge" || m.confirm.island != "alpha" {
		t.Fatalf("a guarded purge should arm a force-purge confirm, got %+v", m.confirm)
	}
	prompt := m.renderConfirm()
	if !strings.Contains(prompt, "unpushed/uncommitted work") || !strings.Contains(prompt, "Force-purge") {
		t.Errorf("force-purge prompt should warn about lost work: %q", prompt)
	}
}

// TestTUISettingsKeysBoth: both 's' and 'S' open Settings (S used to be SSH
// setup — that moved into the Server menu [H] / actions menu [m]).
func TestTUISettingsKeysBoth(t *testing.T) {
	for _, k := range []string{"s", "S"} {
		m := driveKeys(t, seededModel(t, island("alpha")), k)
		if m.settings == nil {
			t.Errorf("%q should open Settings, got settings=nil", k)
		}
		if m.confirm != nil {
			t.Errorf("%q should not arm a confirm, got %+v", k, m.confirm)
		}
	}
}

// TestTUISetupSSHGate: the SSH-setup helper (reached from the Server menu [H] and
// the per-row [m] menu) refuses when the façade is off and arms the account-wide
// setup-ssh confirm when an SSH address is present.
func TestTUISetupSSHGate(t *testing.T) {
	// Façade off: no overview / no SSHAddr.
	res, _ := seededModel(t, island("alpha")).startSSHSetup()
	m := res.(tuiModel)
	if m.confirm != nil {
		t.Errorf("SSH setup without the façade should not arm a confirm, got %+v", m.confirm)
	}
	if !strings.Contains(m.lastError, "ssh façade is off") {
		t.Errorf("SSH setup without the façade should explain it's off, got %q", m.lastError)
	}

	// Façade on.
	m2 := seededModel(t, island("alpha"))
	m2.overview = &api.OverviewResponse{SSHAddr: "minion:2222"}
	res2, _ := m2.startSSHSetup()
	m2 = res2.(tuiModel)
	if m2.confirm == nil || m2.confirm.verb != "setup-ssh" {
		t.Fatalf("SSH setup with the façade on should arm setup-ssh, got %+v", m2.confirm)
	}
	if !strings.Contains(m2.renderConfirm(), "Authorize this machine") {
		t.Errorf("setup-ssh prompt: %q", m2.renderConfirm())
	}
}

// TestTUIUpdateKeysClientOnly: BOTH 'u' and 'U' route to the CLIENT update only
// and NEVER to the daemon update — even when the daemon is behind. The daemon
// self-update lives exclusively in the Server menu [H]. This is the core
// safety guarantee: a routine "get latest" keypress can never restart the daemon
// (which would drop every attached terminal fleet-wide).
// TestTUIUpdateKeys: u/U update the client first, then the daemon when the
// client is current and the daemon is behind. The daemon update still goes
// through the update-daemon confirm (which carries the fleet-wide-restart
// warning + defer-while-attached gate) — it's consented, not a surprise.
func TestTUIUpdateKeys(t *testing.T) {
	for _, k := range []string{"u", "U"} {
		// Client behind → arms update-client (takes precedence over the daemon).
		m := seededModel(t, island("alpha"))
		m.clientUpdate = true
		m = driveKeys(t, m, k)
		if m.confirm == nil || m.confirm.verb != "update-client" {
			t.Fatalf("%q with a client update should arm update-client, got %+v", k, m.confirm)
		}

		// Daemon behind ONLY → arms the (warned + gated) update-daemon confirm.
		m2 := seededModel(t, island("alpha"))
		m2.daemonUpdate = true
		m2 = driveKeys(t, m2, k)
		if m2.confirm == nil || m2.confirm.verb != "update-daemon" {
			t.Fatalf("%q with only a daemon update should arm update-daemon, got %+v", k, m2.confirm)
		}

		// Both behind → the client goes first (never jumps straight to the daemon).
		m3 := seededModel(t, island("alpha"))
		m3.clientUpdate, m3.daemonUpdate = true, true
		m3 = driveKeys(t, m3, k)
		if m3.confirm == nil || m3.confirm.verb != "update-client" {
			t.Fatalf("%q with both behind should arm update-client first, got %+v", k, m3.confirm)
		}
	}
}

// TestTUIServerMenuDaemonUpdate: the Server menu [H] is where the daemon
// self-update lives. It only offers it when the daemon is behind, its confirm
// carries the fleet-wide-restart warning, and otherwise it shows a disabled
// "up to date" line that can't arm anything.
func TestTUIServerMenuDaemonUpdate(t *testing.T) {
	// Daemon behind: the menu's Update-daemon item arms the warned confirm.
	m := seededModel(t, island("alpha"))
	m.daemonUpdate = true
	m.latestRelease = "v9.9.9"
	m = m.openServerMenu()
	if m.menu == nil || !m.menu.global {
		t.Fatal("H should open the global Server menu")
	}
	idx := -1
	for i, it := range m.menu.items {
		if strings.HasPrefix(it.label, "Update daemon (") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("Server menu should offer 'Update daemon' when the daemon is behind")
	}
	m.menu.sel = idx
	res, _ := m.actionMenuKey(key("enter"))
	m = res.(tuiModel)
	if m.confirm == nil || m.confirm.verb != "update-daemon" {
		t.Fatalf("selecting Update daemon should arm update-daemon, got %+v", m.confirm)
	}
	prompt := m.renderConfirm()
	if !strings.Contains(prompt, "RESTARTS it") || !strings.Contains(prompt, "fleet-wide") {
		t.Errorf("daemon-update confirm must warn about the fleet-wide restart, got %q", prompt)
	}

	// Daemon up to date: the item is disabled and selecting it does nothing.
	m2 := seededModel(t, island("alpha"))
	m2 = m2.openServerMenu()
	var disabled *actionMenuItem
	for i := range m2.menu.items {
		if strings.HasPrefix(m2.menu.items[i].label, "Update daemon") {
			disabled = &m2.menu.items[i]
		}
	}
	if disabled == nil || !disabled.disabled {
		t.Fatalf("with the daemon up to date the Update-daemon line should be disabled, got %+v", disabled)
	}
	res2, _ := m2.chooseMenuItem(*disabled)
	m2 = res2.(tuiModel)
	if m2.confirm != nil {
		t.Errorf("a disabled menu line must not arm anything, got %+v", m2.confirm)
	}
}

// TestTUIActionMenuFullVerbs: the per-row island action menu lists the full set
// of lifecycle/setup verbs the §17 row enumerates.
func TestTUIActionMenuFullVerbs(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("m"))
	// A running island offers hibernate; reset/upgrade/rename/purge are always there.
	waitForAll(t, tm, "Hibernate", "Reset agent state", "Upgrade", "Rename", "Purge island")

	tm.Send(key("esc"))
	tm.Send(key("q"))
}

// TestTUIActionMenuWakeOnHibernated: a hibernated island's menu offers Wake (not
// Hibernate) — the menu adapts to container state.
func TestTUIActionMenuWakeOnHibernated(t *testing.T) {
	isl := island("sleepy")
	isl.Container = "hibernated"
	tm := runModel(t, seededModel(t, isl))
	waitForAll(t, tm, "sleepy")

	tm.Send(key("m"))
	waitForAll(t, tm, "Wake")

	tm.Send(key("esc"))
	tm.Send(key("q"))
}

// TestWindowLabelManualNames: a new tab's title is built from the user's
// manually-set names — the island Title (over its slug) and the agent Label
// (over its id) — the §17 "new-tab launches with manual names" row.
func TestWindowLabelManualNames(t *testing.T) {
	m := seededModel(t)
	isl := api.IslandInfo{Name: "proj-slug", Title: "My Project", Container: "running"}
	isl.Agents = []api.AgentInfo{
		{ID: "a1", Type: "claude-code", Label: "backend"},
		{ID: "a2", Type: "claude-code"}, // no label → falls back to id
	}
	m.islands = sortIslands([]api.IslandInfo{isl})

	if got := m.windowLabel("proj-slug", "a1", ""); got != "My Project/backend" {
		t.Errorf("labelled agent: got %q, want %q", got, "My Project/backend")
	}
	if got := m.windowLabel("proj-slug", "a2", ""); got != "My Project/a2" {
		t.Errorf("unlabelled agent should fall back to id: got %q, want %q", got, "My Project/a2")
	}
	// An explicit agentLabel override wins (used right after adding an agent,
	// before the island list refreshes).
	if got := m.windowLabel("proj-slug", "a3", "frontend"); got != "My Project/frontend" {
		t.Errorf("override label: got %q, want %q", got, "My Project/frontend")
	}
	// Unknown island → the raw name is used as the island part.
	if got := m.windowLabel("ghost", "", ""); got != "ghost" {
		t.Errorf("unknown island: got %q, want %q", got, "ghost")
	}
}
