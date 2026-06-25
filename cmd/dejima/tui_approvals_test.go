package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/link"
)

func benign() link.ActionRequest {
	return link.ActionRequest{ID: "act1", From: "web", FromAgent: "w1", To: "infra", ToAgent: "i1", Action: "status", Tier: link.TierBenign}
}
func destructive() link.ActionRequest {
	return link.ActionRequest{ID: "act2", From: "web", FromAgent: "w1", To: "infra", ToAgent: "i1", Action: "purge", Tier: link.TierDestructive, Topic: "ops", Params: `{"target":"db"}`}
}

// TestApprovalsAnnouncement: nothing pending → no banner; pending → a [V] review
// banner; any destructive → red (styleErrorBroadcast) and says "destructive".
func TestApprovalsAnnouncement(t *testing.T) {
	if _, _, _, ok := (tuiModel{}).announcement(); ok {
		t.Fatal("no pending actions should not raise the approvals banner")
	}
	// Benign-only → amber.
	m := tuiModel{pendingActions: []link.ActionRequest{benign()}}
	full, _, st, ok := m.announcement()
	if !ok || !strings.Contains(full, "[V] review") {
		t.Fatalf("benign pending: want a [V] review banner, got ok=%v full=%q", ok, full)
	}
	if fmt.Sprint(st.GetBackground()) != fmt.Sprint(styleBroadcast.GetBackground()) {
		t.Errorf("benign pending should be amber (styleBroadcast)")
	}
	// Any destructive → red + the word "destructive".
	m = tuiModel{pendingActions: []link.ActionRequest{benign(), destructive()}}
	full, _, st, _ = m.announcement()
	if !strings.Contains(full, "destructive") {
		t.Errorf("destructive pending banner should say so: %q", full)
	}
	if fmt.Sprint(st.GetBackground()) != fmt.Sprint(styleErrorBroadcast.GetBackground()) {
		t.Errorf("destructive pending should be red (styleErrorBroadcast)")
	}
}

// TestApprovalsRender: the queue shows each action's route + tier; empty shows
// "nothing pending".
func TestApprovalsRender(t *testing.T) {
	empty := tuiModel{approvals: &approvalsView{}, width: 100}
	if !strings.Contains(plain(empty.renderApprovalsView()), "nothing pending") {
		t.Error("empty queue should say nothing pending")
	}
	m := tuiModel{approvals: &approvalsView{sel: 1, viewing: true}, width: 100,
		pendingActions: []link.ActionRequest{benign(), destructive()}}
	bare := plain(m.renderApprovalsView())
	for _, want := range []string{"web/w1 → status → infra/i1", "purge", "destructive", "params", "[a] approve"} {
		if !strings.Contains(bare, want) {
			t.Errorf("approvals render missing %q:\n%s", want, bare)
		}
	}
}

// TestApprovalsKeyApprove: a destructive approve detours through a typed confirm;
// a non-destructive approve fires immediately; deny fires; esc closes.
func TestApprovalsKeyApprove(t *testing.T) {
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	// Destructive selected → confirm, no immediate command.
	m := tuiModel{approvals: &approvalsView{sel: 0}, pendingActions: []link.ActionRequest{destructive()}}
	out, cmd := m.approvalsKey(key("a"))
	gm := out.(tuiModel)
	if gm.confirm == nil || gm.confirm.verb != "approve-action" || gm.confirm.agent != "act2" {
		t.Errorf("destructive approve should open the approve-action confirm, got %+v", gm.confirm)
	}
	if cmd != nil {
		t.Error("destructive approve must NOT fire immediately (needs typed confirm)")
	}

	// Benign selected → approve fires immediately, no confirm.
	m = tuiModel{approvals: &approvalsView{sel: 0}, pendingActions: []link.ActionRequest{benign()}}
	out, cmd = m.approvalsKey(key("a"))
	if out.(tuiModel).confirm != nil {
		t.Error("benign approve should not require a confirm")
	}
	if cmd == nil {
		t.Error("benign approve should fire a command")
	}

	// Deny fires a command.
	if _, cmd := m.approvalsKey(key("d")); cmd == nil {
		t.Error("deny should fire a command")
	}
	// esc closes.
	if out, _ := m.approvalsKey(tea.KeyMsg{Type: tea.KeyEsc}); out.(tuiModel).approvals != nil {
		t.Error("esc should close the approvals overlay")
	}
}
