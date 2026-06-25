package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/policy"
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

	// Deny opens an (optional) reason prompt rather than firing immediately.
	out, _ = m.approvalsKey(key("d"))
	if c := out.(tuiModel).confirm; c == nil || c.verb != "deny-action" {
		t.Errorf("deny should open the deny-action reason prompt, got %+v", out.(tuiModel).confirm)
	}
	// esc closes.
	if out, _ := m.approvalsKey(tea.KeyMsg{Type: tea.KeyEsc}); out.(tuiModel).approvals != nil {
		t.Error("esc should close the approvals overlay")
	}
}

// TestConfirmModalTopmostOverOverlay guards the fix where a confirm opened from
// the approvals overlay was unreachable: with the overlay still open, typing and
// Enter must route to the confirm modal, not be swallowed by approvalsKey.
func TestConfirmModalTopmostOverOverlay(t *testing.T) {
	m := initialTUIModel(nil)
	m.approvals = &approvalsView{}
	m.pendingActions = []link.ActionRequest{{ID: "act-1", From: "a", To: "b", Action: "deploy", Tier: link.TierMutating}}
	m.confirm = &confirmPrompt{verb: "deny-action", agent: "act-1"}

	// Typing a rune appends to the confirm answer (it would be a revoke/etc in the
	// overlay) — proves the confirm guard preempts the overlay guard.
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	gm := out.(tuiModel)
	if gm.confirm == nil || gm.confirm.answer != "x" {
		t.Fatalf("typing should reach the confirm modal, got %+v", gm.confirm)
	}
	// Enter fires the confirmed command and closes the modal.
	out2, cmd := gm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if out2.(tuiModel).confirm != nil {
		t.Error("Enter should close the confirm modal")
	}
	if cmd == nil {
		t.Error("Enter should fire the confirmed (deny) command")
	}
}

// TestApprovalsRuleFlow: [r] approve+rule is offered on non-destructive actions
// (opens the rule prompt) but never on destructive ones; the spec parses; and
// the rules region revokes immediately.
func TestApprovalsRuleFlow(t *testing.T) {
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	// Non-destructive → [r] opens the approve-rule prompt.
	m := tuiModel{approvals: &approvalsView{sel: 0}, pendingActions: []link.ActionRequest{benign()}}
	out, _ := m.approvalsKey(key("r"))
	if c := out.(tuiModel).confirm; c == nil || c.verb != "approve-rule" || c.agent != "act1" {
		t.Errorf("benign [r] should open the approve-rule prompt, got %+v", out.(tuiModel).confirm)
	}
	// Destructive → [r] is a no-op (a rule can never match it).
	m = tuiModel{approvals: &approvalsView{sel: 0}, pendingActions: []link.ActionRequest{destructive()}}
	if out, _ := m.approvalsKey(key("r")); out.(tuiModel).confirm != nil {
		t.Error("destructive [r] must not offer a rule")
	}

	// parseRuleSpec: "<max> [<ttl>]".
	if mc, ttl := parseRuleSpec("20 1h"); mc != 20 || ttl != "1h" {
		t.Errorf(`parseRuleSpec("20 1h") = (%d,%q), want (20,"1h")`, mc, ttl)
	}
	if mc, ttl := parseRuleSpec(""); mc != 0 || ttl != "" {
		t.Errorf(`parseRuleSpec("") = (%d,%q), want (0,"")`, mc, ttl)
	}

	// Rules region: Tab focuses it (rules present), [x] fires a revoke command.
	m = tuiModel{approvals: &approvalsView{}, policyRules: []policy.Rule{{From: "web", To: "infra", Action: "deploy"}}}
	out, _ = m.approvalsKey(tea.KeyMsg{Type: tea.KeyTab})
	if out.(tuiModel).approvals.focus != focusRules {
		t.Fatal("Tab should focus the rules region when rules exist")
	}
	if _, cmd := out.(tuiModel).approvalsKey(key("x")); cmd == nil {
		t.Error("[x] in the rules region should fire a revoke command")
	}
}
