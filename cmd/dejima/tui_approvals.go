package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aoos/dejima/internal/link"
)

// approvalsView is the action-gate approvals overlay (opened with V): the queue
// of cross-island actions awaiting a human decision (Lane 5 P3). It reads the
// polled m.pendingActions snapshot; approve/deny issue commands that refetch.
// The daemon gate is the source of truth — this is a thin client. A destructive
// approval requires a typed confirm so it can't be rubber-stamped (the gate
// guarantees destructive actions never auto-approve, so they always land here).
//
// Slice-3 follow-ons (when the backend lands policy CRUD + deny-reason): an
// active-rules section, [r] approve+rule, and a deny reason. See
// docs/action-gate-tui-client.md.
type approvalsView struct {
	sel     int
	viewing bool // [v] expanded detail of the selected action
}

type pendingActionsMsg []link.ActionRequest

// fetchPendingActionsCmd polls the action-gate queue. Any error (gate disabled,
// older daemon, transient) collapses to an empty queue rather than surfacing —
// the badge just stays hidden.
func (m tuiModel) fetchPendingActionsCmd() tea.Cmd {
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

func (m tuiModel) approveActionCmd(id string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return opCompleteMsg{name: id, verb: "approve action", err: c.ApproveAction(ctx, id)}
	}
}

func (m tuiModel) denyActionCmd(id string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return opCompleteMsg{name: id, verb: "deny action", err: c.DenyAction(ctx, id)}
	}
}

// selectedAction returns the highlighted pending action, or ok=false if none.
func (m tuiModel) selectedAction() (link.ActionRequest, bool) {
	if m.approvals == nil || m.approvals.sel < 0 || m.approvals.sel >= len(m.pendingActions) {
		return link.ActionRequest{}, false
	}
	return m.pendingActions[m.approvals.sel], true
}

// approvalsKey drives the approvals overlay: navigate the queue, approve/deny/
// view the selected action, refresh, close. A destructive approve detours
// through a typed confirm (runConfirmed verb "approve-action").
func (m tuiModel) approvalsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.approvals
	n := len(m.pendingActions)
	switch msg.String() {
	case "esc", "q", "V":
		m.approvals = nil
		return m, nil
	case "r":
		return m, m.fetchPendingActionsCmd()
	case "j", "down":
		if v.sel < n-1 {
			v.sel++
		}
		return m, nil
	case "k", "up":
		if v.sel > 0 {
			v.sel--
		}
		return m, nil
	case "g", "home":
		v.sel = 0
		return m, nil
	case "end":
		v.sel = max(0, n-1)
		return m, nil
	case "v":
		v.viewing = !v.viewing
		return m, nil
	case "a":
		a, ok := m.selectedAction()
		if !ok {
			return m, nil
		}
		if a.Tier == link.TierDestructive {
			m.confirm = &confirmPrompt{verb: "approve-action", agent: a.ID}
			return m, nil
		}
		return m, m.approveActionCmd(a.ID)
	case "d":
		if a, ok := m.selectedAction(); ok {
			return m, m.denyActionCmd(a.ID)
		}
		return m, nil
	}
	return m, nil
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

	if len(m.pendingActions) == 0 {
		b.WriteString(styleRunning.Render("✓ nothing pending"))
		b.WriteString("\n\n" + styleMuted.Render("Agents request cross-island actions through the gate; each waits here for your call.   [esc] close"))
		return b.String()
	}

	for i, a := range m.pendingActions {
		word := string(a.Tier)
		if a.Tier == link.TierDestructive {
			word = "⚠ " + word
		}
		tier := tierStyle(a.Tier).Render(fmt.Sprintf("%-13s", word))
		route := fmt.Sprintf("%s/%s → %s → %s/%s", a.From, a.FromAgent, styleAccent.Render(a.Action), a.To, a.ToAgent)
		line := fmt.Sprintf("%s  %s  %s", tier, route, styleMuted.Render(timeAgo(a.CreatedAt)))
		if i == m.approvals.sel {
			line = styleSelected.Render("▶ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
		// Expanded detail for the selected action (the full payload — never approve
		// blind): the granted channel + the action params.
		if i == m.approvals.sel && m.approvals.viewing {
			b.WriteString(styleMuted.Render(fmt.Sprintf("      topic:  %s\n", a.Topic)))
			params := a.Params
			if params == "" {
				params = "(none)"
			}
			b.WriteString(styleMuted.Render("      params: ") + truncate(params, max(20, m.width-20)) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("[a] approve   [d] deny   [v] details   [r] refresh   [esc] close   ·   destructive needs a typed y"))
	return b.String()
}
