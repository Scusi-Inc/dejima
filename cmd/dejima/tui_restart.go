package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
}

// openRestartView builds the checklist from the island's current agents, all
// pre-selected (the common case is "apply to everything"), resume on.
func (m tuiModel) openRestartView(island string) tuiModel {
	rv := &restartView{island: island, resume: true}
	if isl, ok := m.islandByName(island); ok {
		for _, a := range isl.Agents {
			label := a.Label
			if label == "" {
				label = a.ID
			}
			rv.items = append(rv.items, restartItem{id: a.ID, label: label, agentType: a.Type, selected: true})
		}
	}
	m.restartPane = rv
	return m
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
	b.WriteString(styleMuted.Render("Needs the island's secrets mount. First-ever secret here? Press [!] to recreate instead."))
	b.WriteString("\n\n")

	if len(v.items) == 0 {
		b.WriteString(styleMuted.Render("no agents in this island"))
		b.WriteString("\n")
	}
	for i, it := range v.items {
		lead := "   "
		if i == v.cursor {
			lead = styleAccent.Render(" ▸ ")
		}
		box := "[ ]"
		if it.selected {
			box = styleRunning.Render("[✓]")
		}
		b.WriteString(lead + box + " " + it.label + styleMuted.Render("  ("+it.agentType+")") + "\n")
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
