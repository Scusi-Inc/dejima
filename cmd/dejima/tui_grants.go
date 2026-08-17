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
	// pending is "grant" or "revoke" while the host-GitHub action awaits
	// confirmation, "" otherwise. The host operator's login reads their whole
	// account, so granting it is not a single-keystroke action.
	pending string
	// notice reports the outcome of the last action.
	notice string
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

// hostGHActionMsg carries the outcome of a grant/revoke back to the pane.
type hostGHActionMsg struct {
	action string
	err    error
}

// hostGHActionCmd performs the grant or revoke, then the caller reloads so the
// pane shows daemon state rather than what it assumed the daemon did.
func (m tuiModel) hostGHActionCmd(island, action string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		if action == "grant" {
			_, err = c.GrantHostGitHubCredential(ctx, island)
		} else {
			err = c.RevokeHostGitHubCredential(ctx, island)
		}
		return hostGHActionMsg{action: action, err: err}
	}
}

func (v *grantsView) applyHostGHAction(msg hostGHActionMsg) {
	if msg.err != nil {
		v.notice = msg.action + " failed: " + msg.err.Error()
		return
	}
	// The credential is a bind mount, so it is fixed at container create. Saying
	// so here avoids the "I granted it and nothing changed" loop.
	v.notice = msg.action + "ed — takes effect when the container is next created (dejima upgrade " + v.island + ")"
	v.loading = true
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
	// A pending host-GitHub action owns the next keystroke: y/enter confirms,
	// anything else cancels. Granting the host operator's login is account-wide
	// read access, so it does not happen on a stray keypress.
	if v.pending != "" {
		action := v.pending
		v.pending = ""
		switch msg.String() {
		case "y", "Y", "enter":
			v.notice = ""
			return m, m.hostGHActionCmd(v.island, action)
		default:
			v.notice = "cancelled"
			return m, nil
		}
	}
	switch msg.String() {
	case "esc", "q", "T":
		m.grants = nil
		return m, nil
	case "G":
		// Only offered where it applies: a tenant island can't hold the host
		// login at all, and the API refuses it, so offering the key there would
		// promise something that cannot happen.
		if v.resp == nil || !v.resp.HostGitHub.Eligible {
			v.notice = "not applicable — this island uses its own GitHub identity"
			return m, nil
		}
		if v.resp.HostGitHub.Granted {
			v.pending = "revoke"
		} else {
			v.pending = "grant"
		}
		return m, nil
	case "r":
		v.loading = true
		v.loadErr = ""
		v.notice = ""
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
	// One list, consulted by everything that summarises. Both the containment
	// claim below and the tally in the footer read from it, so a sixth grant kind
	// is added in ONE place instead of being silently omitted from whichever
	// summary its author didn't think to look at. That omission has already
	// happened twice with the fifth kind (the host GitHub credential): once in
	// the containment claim, and once — in the very commit that fixed it — in the
	// footer tally ten lines below.
	kinds := grantKinds(r)
	total := 0
	for _, k := range kinds {
		total += k.n
	}
	if total == 0 {
		// The locked-down default — make it unmistakable and reassuring. The
		// GitHub clause is stated positively rather than omitted: a deliberate
		// deny is a fact worth showing, not an absence to infer.
		b.WriteString(styleRunning.Render("✓ fully contained — no host files, MCP servers, links, or capabilities granted"))
		b.WriteString("\n")
		b.WriteString(styleRunning.Render("  and no GitHub credential beyond its own identity"))
		b.WriteString(m.renderHostGHFooter())
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

	section("GitHub credential", hostGHRows(r.HostGitHub))

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

	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k.label, k.n))
	}
	hint := strings.Join(parts, " · ")
	if len(lines) > visible {
		hint = fmt.Sprintf("↕ %d/%d   ·   ", v.scroll+1, len(lines)) + hint
	}
	b.WriteString("\n" + styleMuted.Render(hint))
	b.WriteString(m.renderHostGHFooter())
	return b.String()
}

// grantKind is one category of thing an island can reach, with how many of it
// this island holds.
type grantKind struct {
	label string
	n     int
}

// grantKinds enumerates every grant category, and is the ONLY place that does.
// Anything that summarises — "is this island contained", the footer tally —
// derives from this, so adding a category means editing one function rather
// than remembering every arithmetic site that should have counted it.
//
// The host GitHub credential is a yes/no rather than a list, so it contributes
// 0 or 1. It belongs here regardless: it is the widest-reaching of the kinds
// (account-wide read), and leaving it out of the sum is precisely how an island
// holding the operator's entire account came to be reported "fully contained".
func grantKinds(r *api.IslandGrantsResponse) []grantKind {
	hostGH := 0
	if r.HostGitHub.Granted {
		hostGH = 1
	}
	return []grantKind{
		{"Port", len(r.Port)},
		{"MCP", len(r.MCP)},
		{"Links", len(r.Links)},
		{"Capabilities", len(r.Capability)},
		{"GitHub", hostGH},
	}
}

// hostGHRows renders the GitHub-credential section. The three states have to be
// told apart on sight: granted deliberately, granted by the migration and never
// since decided, and denied. Denied is the DEFAULT and is rendered as a normal
// fact — not amber, not an error — because a contained island is the intended
// resting state, not a problem to fix.
func hostGHRows(v api.HostGitHubCredentialView) []string {
	switch {
	case !v.Eligible:
		// A tenant island: the host login is not on offer here at all, and
		// saying "denied" would imply a grant is the missing piece.
		return []string{"  " + styleMuted.Render("n/a — this island clones and pushes as its own GitHub identity")}
	case v.Grandfathered:
		return []string{
			"  " + styleWaiting.Render("⚠ the host operator's login — reads EVERY private repo on that account"),
			"  " + styleMuted.Render("grandfathered "+v.GrantedAt.Format("2006-01-02")+" — carried over by the migration, not yet decided"),
		}
	case v.Granted:
		by := v.GrantedBy
		if by == "" {
			by = "the host operator"
		}
		return []string{
			"  " + styleWaiting.Render("the host operator's login — reads EVERY private repo on that account"),
			"  " + styleMuted.Render("granted by "+by+" on "+v.GrantedAt.Format("2006-01-02")),
		}
	default:
		return []string{
			"  " + styleMuted.Render("none — this island has no GitHub credential of its own"),
			"  " + styleMuted.Render("clone/push of a private repo will fail until it gets one"),
		}
	}
}

// renderHostGHFooter renders the action line, including the confirmation
// prompt. Split out so the fully-contained early return offers the same keys as
// the full pane — an island with nothing granted is exactly where an operator
// goes looking for how to grant something.
func (m tuiModel) renderHostGHFooter() string {
	v := m.grants
	var b strings.Builder
	if v.pending != "" {
		q := "Grant this island the host operator's GitHub login? It reads every private repo on that account."
		if v.pending == "revoke" {
			q = "Revoke the host GitHub credential from this island?"
		}
		b.WriteString("\n" + styleWaiting.Render("  "+q+"  [y] confirm · any other key cancels"))
		return b.String()
	}
	if v.notice != "" {
		b.WriteString("\n" + styleRunning.Render("  "+truncate(v.notice, max(20, m.width-6))))
	}
	keys := "[r] refresh   [esc] close"
	if v.resp != nil && v.resp.HostGitHub.Eligible {
		if v.resp.HostGitHub.Granted {
			keys = "[G] revoke host GitHub credential   " + keys
		} else {
			keys = "[G] grant host GitHub credential   " + keys
		}
	} else if v.resp != nil {
		// Tenant island: name the remedy that IS available, so "no credential"
		// doesn't dead-end. A grant here would be refused by the daemon.
		keys = styleMuted.Render("give it an identity: dejima github connect") + "   " + keys
	}
	b.WriteString("\n" + styleMuted.Render(keys))
	return b.String()
}
