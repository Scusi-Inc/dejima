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

// TestTUIUpgradeConfirm: 'u' on an island arms an upgrade (recreate-on-image)
// confirm.
func TestTUIUpgradeConfirm(t *testing.T) {
	m := driveKeys(t, seededModel(t, island("alpha")), "u")
	if m.confirm == nil || m.confirm.verb != "upgrade" {
		t.Fatalf("u should arm an upgrade confirm, got %+v", m.confirm)
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

// TestTUISetupSSHRequiresFacade: 'S' with the SSH façade off surfaces a helpful
// error and does NOT arm the confirm; with an SSH address present it arms the
// account-wide setup-ssh confirm.
func TestTUISetupSSHRequiresFacade(t *testing.T) {
	// Façade off: no overview / no SSHAddr.
	m := driveKeys(t, seededModel(t, island("alpha")), "S")
	if m.confirm != nil {
		t.Errorf("S without the façade should not arm a confirm, got %+v", m.confirm)
	}
	if !strings.Contains(m.lastError, "ssh façade is off") {
		t.Errorf("S without the façade should explain it's off, got %q", m.lastError)
	}

	// Façade on.
	m2 := seededModel(t, island("alpha"))
	m2.overview = &api.OverviewResponse{SSHAddr: "minion:2222"}
	m2 = driveKeys(t, m2, "S")
	if m2.confirm == nil || m2.confirm.verb != "setup-ssh" {
		t.Fatalf("S with the façade on should arm setup-ssh, got %+v", m2.confirm)
	}
	if !strings.Contains(m2.renderConfirm(), "Authorize this machine") {
		t.Errorf("setup-ssh prompt: %q", m2.renderConfirm())
	}
}

// TestTUIUpdateActions: 'U' arms an update-client confirm when the client is
// behind, an update-daemon confirm when only the daemon is behind, and notices
// "already up to date" otherwise — distinct from lowercase 'u' (island upgrade).
func TestTUIUpdateActions(t *testing.T) {
	// Client behind.
	m := seededModel(t, island("alpha"))
	m.clientUpdate = true
	m = driveKeys(t, m, "U")
	if m.confirm == nil || m.confirm.verb != "update-client" {
		t.Fatalf("U with a client update should arm update-client, got %+v", m.confirm)
	}

	// Daemon behind only.
	m2 := seededModel(t, island("alpha"))
	m2.daemonUpdate = true
	m2 = driveKeys(t, m2, "U")
	if m2.confirm == nil || m2.confirm.verb != "update-daemon" {
		t.Fatalf("U with a daemon update should arm update-daemon, got %+v", m2.confirm)
	}

	// Up to date.
	m3 := driveKeys(t, seededModel(t, island("alpha")), "U")
	if m3.confirm != nil {
		t.Errorf("U with nothing to update should not arm a confirm, got %+v", m3.confirm)
	}
	if !strings.Contains(m3.lastNotice, "up to date") {
		t.Errorf("U up-to-date notice: %q", m3.lastNotice)
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
