package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/policy"
)

// approvalsView is the action-gate approvals overlay (opened with V): the queue
// of cross-island actions awaiting a human decision plus the active auto-approve
// rules (Lane 5 P3). It reads the polled m.pendingActions snapshot and the
// m.policyRules loaded on open; approve/deny/rule mutations issue commands that
// refetch. The daemon gate is the source of truth — this is a thin client.
//
// Two focus regions toggled with Tab: the pending queue (a/d/r/v) and the rules
// list (x to revoke). A destructive approval requires a typed confirm so it
// can't be rubber-stamped (the gate guarantees destructive never auto-approves,
// so it always lands here); a destructive action is never offered [r] (a rule
// could never match it). See docs/action-gate-tui-client.md.
type approvalsView struct {
	sel     int  // cursor in the pending queue
	ruleSel int  // cursor in the rules list
	focus   int  // 0 = pending, 1 = rules
	viewing bool // [v] expanded detail of the selected pending action
}

const (
	focusPending = 0
	focusRules   = 1
)

type pendingActionsMsg []link.ActionRequest
type policyRulesMsg []policy.Rule

// fetchPendingActionsCmd polls the action-gate queue. Any error (gate disabled,
// older daemon, transient) collapses to an empty queue rather than surfacing —
// the badge just stays hidden.
func (m tuiModel) fetchPendingActionsCmd() tea.Cmd {
	if m.demo {
		if !m.demoApprovals {
			return func() tea.Msg { return pendingActionsMsg(nil) }
		}
		return func() tea.Msg { return pendingActionsMsg(demoPending()) }
	}
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		actions, err := c.ListPendingActions(ctx)
		if err != nil {
			return pendingActionsMsg(nil)
		}
		return pendingActionsMsg(actions)
	}
}

// fetchPolicyCmd loads the active auto-approve rules (for the overlay's rules
// section). Loaded on open and after a mutation, not on every tick — rules
// change rarely. Errors collapse to an empty list.
func (m tuiModel) fetchPolicyCmd() tea.Cmd {
	if m.demo {
		return func() tea.Msg { return policyRulesMsg(demoPolicy()) }
	}
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rules, err := c.ListPolicy(ctx)
		if err != nil {
			return policyRulesMsg(nil)
		}
		return policyRulesMsg(rules)
	}
}

func (m tuiModel) approveActionCmd(id string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return opCompleteMsg{name: id, verb: "approve action", err: c.ApproveAction(ctx, id)}
	}
}

func (m tuiModel) denyActionCmd(id, reason string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return opCompleteMsg{name: id, verb: "deny action", err: c.DenyAction(ctx, id, reason)}
	}
}

// approveRuleCmd approves the action AND adds a scoped auto-approve rule for its
// link+action (composing approve + policy.create, per the backend's plan — there
// is no inline endpoint). If the approve fails the rule is not added.
func (m tuiModel) approveRuleCmd(a link.ActionRequest, maxCount int, ttl string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.ApproveAction(ctx, a.ID); err != nil {
			return opCompleteMsg{name: a.ID, verb: "approve action", err: err}
		}
		_, err := c.AddPolicy(ctx, api.PolicyAddRequest{From: a.From, To: a.To, Action: a.Action, MaxCount: maxCount, TTL: ttl})
		return opCompleteMsg{name: a.ID, verb: "add auto-approve rule", err: err}
	}
}

func (m tuiModel) removeRuleCmd(r policy.Rule) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return opCompleteMsg{name: r.Action, verb: "revoke auto-approve rule", err: c.RemovePolicy(ctx, r.From, r.To, r.Action)}
	}
}

// selectedAction returns the highlighted pending action, or ok=false if none.
func (m tuiModel) selectedAction() (link.ActionRequest, bool) {
	if m.approvals == nil || m.approvals.sel < 0 || m.approvals.sel >= len(m.pendingActions) {
		return link.ActionRequest{}, false
	}
	return m.pendingActions[m.approvals.sel], true
}

// findPendingAction looks an action up by id — used at confirm time so a queue
// refresh that reorders the list can't redirect the decision to the wrong action.
func (m tuiModel) findPendingAction(id string) (link.ActionRequest, bool) {
	for _, a := range m.pendingActions {
		if a.ID == id {
			return a, true
		}
	}
	return link.ActionRequest{}, false
}

// approvalsKey drives the approvals overlay. Tab toggles between the pending
// queue and the rules list; each region has its own cursor and actions.
func (m tuiModel) approvalsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.approvals
	switch msg.String() {
	case "esc", "q", "V":
		m.approvals = nil
		return m, nil
	case "tab":
		// Only jump to the rules region if there are rules to act on.
		if v.focus == focusPending && len(m.policyRules) > 0 {
			v.focus = focusRules
		} else {
			v.focus = focusPending
		}
		return m, nil
	}
	if v.focus == focusRules {
		return m.approvalsRulesKey(msg)
	}
	return m.approvalsPendingKey(msg)
}

func (m tuiModel) approvalsPendingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.approvals
	n := len(m.pendingActions)
	switch msg.String() {
	case "j", "down":
		if v.sel < n-1 {
			v.sel++
		}
	case "k", "up":
		if v.sel > 0 {
			v.sel--
		}
	case "g", "home":
		v.sel = 0
	case "end":
		v.sel = max(0, n-1)
	case "v":
		v.viewing = !v.viewing
	case "a":
		a, ok := m.selectedAction()
		if !ok {
			return m, nil
		}
		if a.Tier == link.TierDestructive {
			// Loud typed confirm — never rubber-stamp a destructive action.
			m.confirm = &confirmPrompt{verb: "approve-action", agent: a.ID}
			return m, nil
		}
		return m, m.approveActionCmd(a.ID)
	case "r":
		a, ok := m.selectedAction()
		if !ok || a.Tier == link.TierDestructive {
			// No rule can ever match a destructive action — don't offer one.
			return m, nil
		}
		m.confirm = &confirmPrompt{verb: "approve-rule", agent: a.ID}
		return m, nil
	case "d":
		if a, ok := m.selectedAction(); ok {
			// Deny prompts for an optional reason (recorded in the ledger).
			m.confirm = &confirmPrompt{verb: "deny-action", agent: a.ID}
		}
	}
	return m, nil
}

func (m tuiModel) approvalsRulesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.approvals
	n := len(m.policyRules)
	switch msg.String() {
	case "j", "down":
		if v.ruleSel < n-1 {
			v.ruleSel++
		}
	case "k", "up":
		if v.ruleSel > 0 {
			v.ruleSel--
		}
	case "x", "d":
		// Revoke the selected rule. Revoking only tightens the gate (the safe
		// direction), so it goes through immediately — no typed confirm.
		if v.ruleSel >= 0 && v.ruleSel < n {
			return m, m.removeRuleCmd(m.policyRules[v.ruleSel])
		}
	}
	return m, nil
}

// parseRuleSpec parses the approve+rule answer "<max> [<ttl>]" into a max count
// (0 = unlimited; blank/unparseable → 0) and a TTL string passed through to the
// backend (Go duration like "1h"; blank = no expiry).
func parseRuleSpec(answer string) (maxCount int, ttl string) {
	fields := strings.Fields(answer)
	if len(fields) > 0 {
		maxCount, _ = strconv.Atoi(fields[0])
	}
	if len(fields) > 1 {
		ttl = fields[1]
	}
	return maxCount, ttl
}

// tierStyle maps an action's risk tier to its row style: destructive is loud
// (bold red), mutating amber, benign muted.
func tierStyle(t link.Tier) lipgloss.Style {
	switch t {
	case link.TierDestructive:
		return styleErrored.Bold(true)
	case link.TierMutating:
		return styleWaiting
	default:
		return styleMuted
	}
}

func (m tuiModel) renderApprovalsView() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Action approvals"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("cross-island actions awaiting your decision — destructive never auto-approves"))
	b.WriteString("\n\n")

	// Pending queue.
	b.WriteString(m.sectionHeader("Pending", m.approvals.focus == focusPending))
	b.WriteString("\n")
	if len(m.pendingActions) == 0 {
		b.WriteString("  " + styleMuted.Render("✓ nothing pending") + "\n")
	}
	for i, a := range m.pendingActions {
		word := string(a.Tier)
		if a.Tier == link.TierDestructive {
			word = "⚠ " + word
		}
		tier := tierStyle(a.Tier).Render(fmt.Sprintf("%-13s", word))
		// Show agent NAMES (labels), not bare ids — the operator's roster resolves
		// both islands; the id stays the addressing handle elsewhere.
		route := fmt.Sprintf("%s/%s → %s → %s/%s",
			a.From, m.agentDisplayIn(a.From, a.FromAgent), styleAccent.Render(a.Action),
			a.To, m.agentDisplayIn(a.To, a.ToAgent))
		line := fmt.Sprintf("%s  %s  %s", tier, route, styleMuted.Render(timeAgo(a.CreatedAt)))
		if m.approvals.focus == focusPending && i == m.approvals.sel {
			line = styleSelected.Render("▶ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
		// Expanded detail for the selected action (the full payload — never approve
		// blind): the granted channel + the action params.
		if m.approvals.focus == focusPending && i == m.approvals.sel && m.approvals.viewing {
			b.WriteString(styleMuted.Render(fmt.Sprintf("      topic:  %s\n", a.Topic)))
			params := a.Params
			if params == "" {
				params = "(none)"
			}
			b.WriteString(styleMuted.Render("      params: ") + truncate(params, max(20, m.width-20)) + "\n")
		}
	}

	// Active auto-approve rules.
	b.WriteString("\n")
	b.WriteString(m.sectionHeader("Auto-approve rules", m.approvals.focus == focusRules))
	b.WriteString("\n")
	if len(m.policyRules) == 0 {
		b.WriteString("  " + styleMuted.Render("none — every gated action prompts you (prompt-everything)") + "\n")
	}
	for i, r := range m.policyRules {
		count := "∞"
		if r.MaxCount > 0 {
			count = strconv.Itoa(r.MaxCount)
		}
		exp := "no expiry"
		if !r.ExpiresAt.IsZero() {
			exp = "expires " + timeUntil(r.ExpiresAt)
		}
		row := fmt.Sprintf("%s → %s → %s   %d/%s   %s",
			r.From, styleAccent.Render(r.Action), r.To, r.Used, count, styleMuted.Render(exp))
		if m.approvals.focus == focusRules && i == m.approvals.ruleSel {
			row = styleSelected.Render("▶ " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}

	b.WriteString("\n")
	if m.approvals.focus == focusRules {
		b.WriteString(styleMuted.Render("[x] revoke rule   [tab] pending   [esc] close"))
	} else {
		b.WriteString(styleMuted.Render("[a] approve   [d] deny   [r] approve+rule   [v] details   [tab] rules   [esc] close   ·   destructive needs a typed y"))
	}
	return b.String()
}

// sectionHeader renders a section label, brightened when its region has focus.
func (m tuiModel) sectionHeader(label string, focused bool) string {
	if focused {
		return styleHeader.Render("● " + label)
	}
	return styleMuted.Render("  " + label)
}
