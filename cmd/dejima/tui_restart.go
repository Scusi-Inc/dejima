package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// restartView is the "which agents to restart" checklist, opened from the
// Secrets pane after a change lands. Restarting an agent relaunches it in a
// fresh login shell so it picks up the new environment (a new/rotated secret) —
// the lighter, per-agent alternative to a whole-island recreate. It offers a
// [!] recreate-island escape hatch for the first-ever-secret case, where the
// container has no secrets mount yet and only a recreate can add it.
type restartView struct {
	island string
	items  []restartItem
	cursor int
	resume bool // continue conversations across the restart, where supported
	busy   bool
	notice string
	err    string
}

type restartItem struct {
	id, label, agentType string
	selected             bool
	// busy marks an agent that looks mid-task. A restart kills its process, so
	// an agent that is working loses the turn it's in the middle of. These are
	// listed but NOT pre-selected — see openRestartView.
	busy bool
}

// agentIsBusy reports whether an agent looks like it is mid-task, and so would
// lose work to a restart. It deliberately reuses agentStatus rather than
// re-deriving the state, so the word here is the same word the dashboard row
// shows — an operator who sees "working" on the row sees "working" here too,
// instead of two subtly different notions of busy.
//
// Note the asymmetry with a safety check: a missing signal reads as not-busy,
// because AgentState is only populated on the detail endpoint. Treating unknown
// as busy would hold back every agent whenever detail hasn't loaded, which is
// the common path — and a checklist that pre-selects nothing is one the
// operator learns to [a]-select past without reading. This is a nudge away from
// the obviously-wrong click, not a guard.
func agentIsBusy(a api.AgentInfo) bool {
	word, _ := agentStatus(a)
	return word == "working"
}

// islandWithAgentState returns an island by name, preferring the loaded detail
// over the list. Agent liveness (AgentState) rides only on the detail endpoint,
// so the list copy reports every agent as idle — anything asking "is this agent
// mid-task?" has to look here first or it will always get "no".
func (m tuiModel) islandWithAgentState(name string) (api.IslandInfo, bool) {
	if m.detail != nil && m.detail.Name == name {
		return *m.detail, true
	}
	return m.islandByName(name)
}

// openRestartView builds the checklist from the island's current agents, resume
// on. Everything that is idle is pre-selected (the common case is "apply to
// everything"); anything that looks mid-task is listed unselected, so applying
// a secret never costs someone a turn of work on one keystroke.
func (m tuiModel) openRestartView(island string) tuiModel {
	rv := &restartView{island: island, resume: true}
	isl, ok := m.islandWithAgentState(island)
	if ok {
		for _, a := range isl.Agents {
			label := a.Label
			if label == "" {
				label = a.ID
			}
			busy := agentIsBusy(a)
			rv.items = append(rv.items, restartItem{
				id: a.ID, label: label, agentType: a.Type,
				selected: !busy, busy: busy,
			})
		}
	}
	m.restartPane = rv
	return m
}

// restartAgentCmd restarts a SINGLE agent — the settings-menu path, as opposed
// to the secrets pane's batch checklist. It reports through opCompleteMsg so the
// dashboard clears its dirty marker and refreshes like any other island op.
func (m tuiModel) restartAgentCmd(island, agentID string, resume bool) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		err := c.RestartAgent(ctx, island, agentID, resume)
		notice := ""
		if err == nil {
			if resume {
				notice = "restarted " + agentID + " — continuing its conversation"
			} else {
				notice = "restarted " + agentID + " cold — new conversation"
			}
		}
		return opCompleteMsg{name: island, verb: "restart agent", err: err, notice: notice}
	}
}

func (m tuiModel) restartKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.restartPane
	if v.busy { // a restart is in flight — only allow bailing out of the view
		if msg.String() == "esc" {
			m.restartPane = nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		m.restartPane = nil
		return m, nil
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
		}
		return m, nil
	case "down", "j":
		if v.cursor < len(v.items)-1 {
			v.cursor++
		}
		return m, nil
	case " ":
		if len(v.items) > 0 {
			v.items[v.cursor].selected = !v.items[v.cursor].selected
		}
		return m, nil
	case "a":
		// Select/deselect all: if everything's already selected, clear; else select all.
		all := true
		for _, it := range v.items {
			if !it.selected {
				all = false
				break
			}
		}
		for i := range v.items {
			v.items[i].selected = !all
		}
		return m, nil
	case "g":
		v.resume = !v.resume
		return m, nil
	case "!":
		// Heavier fallback: recreate the whole island. This is the only thing that
		// works for the FIRST secret (no mount yet), and it restarts every agent.
		island := v.island
		m.restartPane = nil
		m.confirm = &confirmPrompt{verb: "recreate-island", island: island}
		return m, nil
	case "enter":
		var ids []string
		for _, it := range v.items {
			if it.selected {
				ids = append(ids, it.id)
			}
		}
		if len(ids) == 0 {
			v.err = "nothing selected — [space] to pick agents, or [esc] to cancel"
			return m, nil
		}
		v.busy, v.err = true, ""
		v.notice = fmt.Sprintf("restarting %d agent(s)…", len(ids))
		return m, m.restartAgentsCmd(v.island, ids, v.resume)
	}
	return m, nil
}

// restartDoneMsg reports the outcome of a batch restart back to the view.
type restartDoneMsg struct {
	island string
	ok     int
	failed []string
	err    error
}

// restartAgentsCmd restarts the chosen agents (sequentially — a recreate storm
// helps no one), collecting per-agent failures rather than aborting on the first.
func (m tuiModel) restartAgentsCmd(island string, ids []string, resume bool) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		res := restartDoneMsg{island: island}
		for _, id := range ids {
			if err := c.RestartAgent(ctx, island, id, resume); err != nil {
				res.failed = append(res.failed, id)
				res.err = err
			} else {
				res.ok++
			}
		}
		return res
	}
}

func (v *restartView) view(width int) string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("Restart agents to apply — " + v.island))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("Relaunches the selected agents in a fresh shell so they pick up new secrets."))
	b.WriteString("\n")
	// Not a hedge — the two cases are real and coexist. An island created after the
	// secrets-mount fix has the DIRECTORY bound, so a restart genuinely applies.
	// One created before it holds the original file's inode for the container's
	// whole life, and no restart of a process inside changes that. Name the second
	// case so the operator who restarts and sees nothing has somewhere to go, and
	// doesn't conclude either that secrets are broken or that it worked.
	b.WriteString(styleMuted.Render("Still stale afterwards, or this is the island's first-ever secret? Then it"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("predates the secrets-mount fix — press [!] to recreate, which always applies."))
	b.WriteString("\n\n")

	if len(v.items) == 0 {
		b.WriteString(styleMuted.Render("no agents in this island"))
		b.WriteString("\n")
	}
	anyBusy := false
	for i, it := range v.items {
		lead := "   "
		if i == v.cursor {
			lead = styleAccent.Render(" ▸ ")
		}
		box := "[ ]"
		if it.selected {
			box = styleRunning.Render("[✓]")
		}
		line := lead + box + " " + it.label + styleMuted.Render("  ("+it.agentType+")")
		if it.busy {
			anyBusy = true
			line += styleWaiting.Render("  ⚠ working — restarting loses this turn")
		}
		b.WriteString(line + "\n")
	}
	// Say why something is unticked. An unexplained empty box reads as an
	// oversight, and the operator's fix for an oversight is [a].
	if anyBusy {
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("Agents marked ⚠ look mid-task, so they're left unticked — tick one only if"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("you're willing to lose the turn it's working on. They keep the OLD secrets"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("until they're restarted."))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	resumeState := "off (cold start)"
	if v.resume {
		resumeState = "on (continue the conversation where supported)"
	}
	b.WriteString(styleMuted.Render("resume: " + resumeState))
	b.WriteString("\n")
	if v.err != "" {
		b.WriteString(styleErrored.Render("⚠ "+v.err) + "\n")
	}
	if v.notice != "" {
		b.WriteString(styleRunning.Render("• "+v.notice) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("[space] toggle   [a] all/none   [g] resume   [⏎] restart selected   [!] recreate island   [esc] cancel"))
	return b.String()
}
