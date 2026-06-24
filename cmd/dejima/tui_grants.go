package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// grantsView is the full-pane "island grants" trust surface (opened with `T` or
// from the action menu). It shows, for one island, everything it can reach —
// host files (Port), MCP servers, inter-island links, and capabilities — so an
// operator can confirm at a glance what an island is and isn't allowed to touch.
// Read-only: granting/revoking lives in the CLI (`dejima port|mcp|link …`). The
// data is fetched once on open from the daemon's aggregate grants endpoint.
type grantsView struct {
	island  string
	loading bool
	loadErr string
	resp    *api.IslandGrantsResponse
	scroll  int
}

// openGrantsView opens the trust surface for the given island (the selected
// island, or the island an agent belongs to). A blank name is a no-op.
func (m tuiModel) openGrantsView(island string) (tea.Model, tea.Cmd) {
	if island == "" {
		return m, nil
	}
	m.grants = &grantsView{island: island, loading: true}
	return m, m.loadGrantsCmd(island)
}

type grantsLoadedMsg struct {
	resp *api.IslandGrantsResponse
	err  error
}

func (m tuiModel) loadGrantsCmd(island string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.ListGrants(ctx, island)
		return grantsLoadedMsg{resp: resp, err: err}
	}
}

func (v *grantsView) applyLoaded(msg grantsLoadedMsg) {
	v.loading = false
	if msg.err != nil {
		v.loadErr = msg.err.Error()
		return
	}
	v.loadErr = ""
	v.resp = msg.resp
	v.scroll = 0
}

// grantsKey drives the trust pane: esc/q/T closes, r refreshes, j/k/arrows and
// page keys scroll. The pane owns every key while open.
func (m tuiModel) grantsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.grants
	switch msg.String() {
	case "esc", "q", "T":
		m.grants = nil
		return m, nil
	case "r":
		v.loading = true
		v.loadErr = ""
		return m, m.loadGrantsCmd(v.island)
	case "j", "down":
		v.scroll++
	case "k", "up":
		if v.scroll > 0 {
			v.scroll--
		}
	case "g", "home":
		v.scroll = 0
	case "pgdown", "ctrl+d":
		v.scroll += 10
	case "pgup", "ctrl+u":
		v.scroll -= 10
		if v.scroll < 0 {
			v.scroll = 0
		}
	}
	return m, nil
}

// renderGrantsView renders the trust pane to fit the available height. The
// caller (View) wraps the result in stylePane.
func (m tuiModel) renderGrantsView() string {
	v := m.grants
	var b strings.Builder
	b.WriteString(styleTitle.Render("Grants · " + v.island))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("what this island can reach — everything not listed is denied"))
	b.WriteString("\n\n")

	switch {
	case v.loading:
		return b.String() + styleMuted.Render("loading…")
	case v.loadErr != "":
		return b.String() + styleErrored.Render("error: "+v.loadErr) +
			"\n\n" + styleMuted.Render("(is the daemon reachable? press r to retry, esc to close)")
	}

	r := v.resp
	total := len(r.Port) + len(r.MCP) + len(r.Links) + len(r.Capability)
	if total == 0 {
		// The locked-down default — make it unmistakable and reassuring.
		b.WriteString(styleRunning.Render("✓ fully contained — no host files, MCP servers, links, or capabilities granted"))
		b.WriteString("\n\n" + styleMuted.Render("[r] refresh   [esc] close"))
		return b.String()
	}

	// Build the body as lines so a heavily-granted island can scroll.
	var lines []string
	section := func(title string, rows []string) {
		lines = append(lines, styleHeader.Render(title))
		if len(rows) == 0 {
			lines = append(lines, "  "+styleMuted.Render("none — deny-all"))
		} else {
			lines = append(lines, rows...)
		}
		lines = append(lines, "")
	}

	var port []string
	for _, p := range r.Port {
		mode := styleMuted.Render("ro")
		if p.Mode == "rw" {
			mode = styleWaiting.Render("rw") // writable is more powerful — flag it amber
		}
		port = append(port, fmt.Sprintf("  %s  %s  %s",
			mode, styleAccent.Render(p.Name), styleMuted.Render(truncate(p.HostPath, max(20, m.width-32)))))
	}
	section("Host files (Port)", port)

	var mcp []string
	for _, g := range r.MCP {
		mcp = append(mcp, "  "+styleAccent.Render(g.Server))
	}
	section("MCP servers", mcp)

	var links []string
	for _, l := range r.Links {
		line := "  " + styleAccent.Render(l.From+" → "+l.To)
		if l.Topic != "" {
			line += styleMuted.Render("  · " + l.Topic)
		}
		links = append(links, line)
	}
	section("Inter-island links", links)

	var caps []string
	for _, c := range r.Capability {
		caps = append(caps, "  "+styleAccent.Render(c.Target))
	}
	section("Capabilities", caps)

	// Window the body to the pane height; the cursor scrolls it.
	const chrome = 6 // title, subtitle, two blanks, footer hint, pane border
	visible := m.height - chrome
	if visible < 3 {
		visible = 3
	}
	if v.scroll > len(lines)-1 {
		v.scroll = max(0, len(lines)-1)
	}
	end := min(v.scroll+visible, len(lines))
	for _, ln := range lines[v.scroll:end] {
		b.WriteString(ln + "\n")
	}

	hint := fmt.Sprintf("Port %d · MCP %d · Links %d · Capabilities %d   ·   [r] refresh   [esc] close",
		len(r.Port), len(r.MCP), len(r.Links), len(r.Capability))
	if len(lines) > visible {
		hint = fmt.Sprintf("↕ %d/%d   ·   ", v.scroll+1, len(lines)) + hint
	}
	b.WriteString("\n" + styleMuted.Render(hint))
	return b.String()
}
