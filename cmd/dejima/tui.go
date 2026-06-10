package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/events"
)

// newTUICmd is the interactive dashboard. Launched by `dejima` with no args.
// One-shot CLI verbs (`dejima ls`, etc.) continue to work for scripting.
func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:    "tui",
		Short:  "Launch the interactive dashboard (default when run with no args).",
		Hidden: true, // not surfaced in `dejima --help`; users get it via bare `dejima`.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context())
		},
	}
}

// runTUI starts the bubbletea program; on Enter, it exits with a saved
// connect-to-this-island intent which the caller acts on after the TUI loop.
func runTUI(ctx context.Context) error {
	c, err := client()
	if err != nil {
		return err
	}
	m := initialTUIModel(c)
	finalRaw, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return err
	}
	final := finalRaw.(tuiModel)
	if final.connectTo != "" {
		// Use the model's client, which may have been swapped via the switcher.
		return runConnectFromTUI(ctx, final.client, final.connectTo)
	}
	return nil
}

func runConnectFromTUI(ctx context.Context, c *api.Client, name string) error {
	info, err := c.GetIsland(ctx, name)
	if err != nil {
		return err
	}
	if info.Container != "running" {
		return fmt.Errorf("island %q is %s; `dejima wake %s` first", name, info.Container, name)
	}
	return runSession(ctx, c, name, defaultLabel())
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type tuiModel struct {
	client *api.Client

	islands  []api.IslandInfo
	overview *api.OverviewResponse
	detail   *api.IslandInfo
	events_  []events.Event

	selected  int
	width     int
	height    int
	lastError string
	connectTo string // set on quit-to-connect; main() acts on this
	confirm   *confirmPrompt
	dirtyOps  map[string]string // name → "hibernating" etc. (transient hint)
	building  bool              // island image build in flight

	help         bool           // help overlay visible
	helpAdvanced bool           // advanced section of the help overlay expanded
	creator      *creatorModel  // non-nil while the new-island flow is active
	switcher     *switcherModel // non-nil while the connection switcher is open

	activeHost  string // current target: "" = local socket, else host:port
	activeLabel string // profile name for the active target, if known
	skew        string // client/daemon version-skew warning, or ""
}

type confirmPrompt struct {
	verb   string // "purge", "reset"
	island string
	answer string
}

func initialTUIModel(c *api.Client) tuiModel {
	return tuiModel{
		client:     c,
		dirtyOps:   map[string]string{},
		activeHost: os.Getenv("DEJIMA_HOST"),
	}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tickMsg time.Time
type listMsg []api.IslandInfo
type overviewMsg *api.OverviewResponse
type detailMsg struct {
	info   *api.IslandInfo
	events []events.Event
}
type errMsg struct{ err error }
type opCompleteMsg struct {
	name string
	verb string
	err  error
}
type imageBuildDoneMsg struct{ err error }

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m tuiModel) fetchListCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		list, err := m.client.ListIslands(ctx)
		if err != nil {
			return errMsg{err}
		}
		return listMsg(list)
	}
}

func (m tuiModel) fetchOverviewCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		o, err := m.client.Overview(ctx)
		if err != nil {
			return errMsg{err}
		}
		return overviewMsg(o)
	}
}

func (m tuiModel) fetchDetailCmd(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		info, err := m.client.GetIsland(ctx, name)
		if err != nil {
			return errMsg{err}
		}
		evs, _ := m.client.IslandEvents(ctx, name)
		return detailMsg{info: info, events: evs}
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd(), tickCmd())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		cmds := []tea.Cmd{tickCmd(), m.fetchListCmd(), m.fetchOverviewCmd()}
		if name := m.selectedName(); name != "" {
			cmds = append(cmds, m.fetchDetailCmd(name))
		}
		return m, tea.Batch(cmds...)

	case listMsg:
		m.islands = sortIslands(msg)
		m.lastError = ""
		if m.selected >= len(m.islands) {
			m.selected = len(m.islands) - 1
		}
		if m.selected < 0 {
			m.selected = 0
		}
		if name := m.selectedName(); name != "" && (m.detail == nil || m.detail.Name != name) {
			return m, m.fetchDetailCmd(name)
		}
		return m, nil

	case overviewMsg:
		m.overview = msg
		if msg != nil {
			m.skew = versionSkew(msg.DaemonVersion, msg.APIVersion)
		}
		return m, nil

	case detailMsg:
		if name := m.selectedName(); msg.info != nil && msg.info.Name == name {
			m.detail = msg.info
			m.events_ = msg.events
		}
		return m, nil

	case errMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
		return m, nil

	case opCompleteMsg:
		delete(m.dirtyOps, msg.name)
		if msg.err != nil {
			m.lastError = fmt.Sprintf("%s %s: %v", msg.verb, msg.name, msg.err)
		}
		return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())

	case imageBuildDoneMsg:
		m.building = false
		if msg.err != nil {
			m.lastError = fmt.Sprintf("image build: %v", msg.err)
		}
		return m, m.fetchOverviewCmd()

	case reposDiscoveredMsg:
		if m.creator != nil {
			m.creator.onReposDiscovered(msg)
		}
		return m, nil

	case repoStatusMsg:
		if m.creator != nil {
			m.creator.onRepoStatus(msg)
		}
		return m, nil

	case islandCreatedMsg:
		if m.creator != nil {
			if msg.err != nil {
				m.creator.creating = false
				m.creator.err = msg.err.Error()
				return m, nil
			}
			m.creator = nil
			m.connectTo = msg.name // drop straight into the new island's session
			return m, tea.Quit
		}
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The new-island creator owns all keys while active.
	if m.creator != nil {
		return m.creatorKey(msg)
	}
	// The connection switcher owns all keys while open.
	if m.switcher != nil {
		return m.switcherKey(msg)
	}
	// The help overlay owns keys while shown.
	if m.help {
		switch msg.String() {
		case "?", "esc", "q":
			m.help = false
		case "a":
			m.helpAdvanced = !m.helpAdvanced
		}
		return m, nil
	}
	// Confirmation modal owns keys when active.
	if m.confirm != nil {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.confirm = nil
			return m, nil
		case "enter":
			c := *m.confirm
			m.confirm = nil
			return m.runConfirmed(c)
		case "backspace":
			if len(m.confirm.answer) > 0 {
				m.confirm.answer = m.confirm.answer[:len(m.confirm.answer)-1]
			}
			return m, nil
		default:
			if len(msg.String()) == 1 {
				m.confirm.answer += msg.String()
			}
			return m, nil
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "n":
		return m.openCreator()
	case "s":
		return m.openSwitcher()
	case "j", "down":
		if m.selected < len(m.islands)-1 {
			m.selected++
			return m, m.fetchDetailCmd(m.selectedName())
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
			return m, m.fetchDetailCmd(m.selectedName())
		}
	case "g", "home":
		m.selected = 0
		return m, m.fetchDetailCmd(m.selectedName())
	case "G", "end":
		m.selected = len(m.islands) - 1
		return m, m.fetchDetailCmd(m.selectedName())
	case "enter", "o":
		// Default behavior: open the island in a new window so the dashboard
		// stays up. When no new-window backend is available (e.g. Linux
		// without tmux), fall back to attaching in-place by quitting the TUI
		// — better than dead-ending the user.
		if name := m.selectedName(); name != "" {
			if m.detail != nil && m.detail.Container != "running" {
				m.lastError = fmt.Sprintf("island %q is %s; `w` to wake it first", name, m.detail.Container)
				return m, nil
			}
			if canOpenNewWindow() {
				if err := m.openInNewWindow(name); err != nil {
					m.lastError = err.Error()
				}
				return m, nil
			}
			m.connectTo = name
			return m, tea.Quit
		}
	case "a":
		// Explicit "attach in this terminal" — replaces the dashboard. Useful
		// when the user actually wants the old behavior even though a
		// new-window backend is available.
		if name := m.selectedName(); name != "" && m.detail != nil && m.detail.Container == "running" {
			m.connectTo = name
			return m, tea.Quit
		}
	case "h":
		if name := m.selectedName(); name != "" {
			m.dirtyOps[name] = "hibernating"
			return m, m.opCmd(name, "hibernate")
		}
	case "w":
		if name := m.selectedName(); name != "" {
			m.dirtyOps[name] = "waking"
			return m, m.opCmd(name, "wake")
		}
	case "r":
		if name := m.selectedName(); name != "" {
			m.confirm = &confirmPrompt{verb: "reset", island: name}
		}
	case "u":
		if name := m.selectedName(); name != "" {
			m.confirm = &confirmPrompt{verb: "upgrade", island: name}
		}
	case "b":
		if !m.building {
			m.confirm = &confirmPrompt{verb: "build-image"}
		}
	case "d":
		if name := m.selectedName(); name != "" {
			m.confirm = &confirmPrompt{verb: "purge", island: name}
		}
	case "R":
		return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
	}
	return m, nil
}

func (m tuiModel) runConfirmed(c confirmPrompt) (tea.Model, tea.Cmd) {
	switch c.verb {
	case "reset":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			m.dirtyOps[c.island] = "resetting"
			return m, m.opCmd(c.island, "reset")
		}
	case "upgrade":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			m.dirtyOps[c.island] = "upgrading"
			return m, m.opCmd(c.island, "upgrade")
		}
	case "build-image":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			m.building = true
			return m, m.buildImageCmd()
		}
	case "purge":
		if strings.TrimSpace(c.answer) == c.island {
			m.dirtyOps[c.island] = "purging"
			return m, m.opCmd(c.island, "purge")
		}
	}
	return m, nil
}

// buildImageCmd rebuilds the island image from the daemon's embedded build
// context. Output is discarded — the footer shows an in-flight indicator and
// the CLI (`dejima image build`) is the place to watch full build logs.
func (m tuiModel) buildImageCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		return imageBuildDoneMsg{err: m.client.BuildImage(ctx, io.Discard)}
	}
}

func (m tuiModel) opCmd(name, verb string) tea.Cmd {
	return func() tea.Msg {
		// Upgrade stops, removes, and recreates the container; give it longer
		// than the quick start/stop verbs.
		timeout := 30 * time.Second
		if verb == "upgrade" {
			timeout = 2 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var err error
		switch verb {
		case "hibernate":
			_, err = m.client.HibernateIsland(ctx, name)
		case "wake":
			_, err = m.client.WakeIsland(ctx, name)
		case "reset":
			_, err = m.client.ResetIsland(ctx, name)
		case "upgrade":
			_, err = m.client.UpgradeIsland(ctx, name)
		case "purge":
			err = m.client.DeleteIsland(ctx, name)
		}
		return opCompleteMsg{name: name, verb: verb, err: err}
	}
}

func (m tuiModel) selectedName() string {
	if m.selected < 0 || m.selected >= len(m.islands) {
		return ""
	}
	return m.islands[m.selected].Name
}

func sortIslands(in []api.IslandInfo) []api.IslandInfo {
	out := append([]api.IslandInfo(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		// Running first, then alphabetic.
		ri := out[i].Container == "running"
		rj := out[j].Container == "running"
		if ri != rj {
			return ri
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	stylePane      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#1c3358")).Padding(0, 1)
	styleHeader    = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8")).Bold(true)
	styleTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#eef3ff")).Bold(true)
	styleMuted     = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8"))
	styleAccent    = lipgloss.NewStyle().Foreground(lipgloss.Color("#e8f1ff"))
	styleSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#1c3358"))
	styleRunning   = lipgloss.NewStyle().Foreground(lipgloss.Color("#34d399"))
	styleHibernate = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8"))
	styleErrored   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f87171"))
	styleWaiting   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24"))
	styleFooter    = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8"))
)

func (m tuiModel) View() string {
	if m.width == 0 {
		return "loading…"
	}

	header := m.renderHeader()
	hh := lipgloss.Height(header)

	// Full-pane overlays take over the body + footer.
	if m.creator != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.creator.view(m.width - 6))
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.switcher != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.switcher.view())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.help {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderHelp())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}

	footer := m.renderFooter()
	body := m.renderBody(hh)

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// asciiLogo is a terminal rendering of assets/logo-transparent.png: the
// island is an annulus sector (parallel top/bottom arcs joined by angled
// sides), with a gate hanging from the bottom arc and a bridge crossing
// beneath the curved shore. All lines are the same printed width so it
// composes as a block.
var asciiLogo = []string{
	" _.------------._ ",
	"  \\            /  ",
	"    \\_.----._/    ",
	"       |  |       ",
	"       |[]|       ",
	"   _.-'|  |'-._   ",
	" .'    |  |    '. ",
}

func (m tuiModel) renderHeader() string {
	label := m.activeLabel
	if label == "" {
		if m.activeHost == "" {
			label = "local"
		} else {
			label = m.activeHost
		}
	}

	// Compact single-line header when the terminal can't spare the rows, or
	// is too narrow for the info lines (longest is 69 cols + 27 logo/chrome).
	if m.height < 24 || m.width < 96 {
		title := styleTitle.Render("Dejima")
		right := styleMuted.Render(label + " ⇄ [s]")
		pad := m.width - lipgloss.Width(title) - lipgloss.Width(right) - 2
		if pad < 1 {
			pad = 1
		}
		return " " + title + strings.Repeat(" ", pad) + right
	}

	logoLines := make([]string, len(asciiLogo))
	for i, l := range asciiLogo {
		logoLines[i] = styleAccent.Render(l)
	}
	logo := strings.Join(logoLines, "\n")

	info := strings.Join([]string{
		styleTitle.Render("Dejima") + styleMuted.Render(" — isolated islands for AI coding agents, on your own hardware"),
		"",
		styleMuted.Render("Each island is one repo + one agent in its own container."),
		styleAccent.Render("↑/↓") + styleMuted.Render(" pick an island  ·  ") + styleAccent.Render("⏎") + styleMuted.Render(" open in a new window  ·  ") + styleAccent.Render("n") + styleMuted.Render(" launch a new one"),
		styleMuted.Render("Close the terminal — agents keep running; reattach from any device."),
		styleMuted.Render("server: ") + styleAccent.Render(label) + styleMuted.Render("  ·  [s] switch  ·  [?] all keys"),
	}, "\n")
	infoW := m.width - lipgloss.Width(asciiLogo[0]) - 9
	info = lipgloss.NewStyle().MaxWidth(infoW).Render(info)

	box := lipgloss.JoinHorizontal(lipgloss.Top, logo, "   ", info)
	return stylePane.Width(m.width - 2).Render(box)
}

func (m tuiModel) renderBody(headerHeight int) string {
	leftW := m.width / 2
	if leftW < 30 {
		leftW = 30
	}
	rightW := m.width - leftW - 4
	if rightW < 20 {
		rightW = 20
	}
	bodyHeight := m.height - headerHeight - 4
	if bodyHeight < 5 {
		bodyHeight = 5
	}

	left := stylePane.Width(leftW).Height(bodyHeight).Render(m.renderList(leftW - 4))
	right := stylePane.Width(rightW).Height(bodyHeight).Render(m.renderDetail(rightW - 4))

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	if m.confirm != nil {
		return body + "\n" + m.renderConfirm()
	}
	return body
}

func (m tuiModel) renderList(_ int) string {
	if len(m.islands) == 0 {
		if m.lastError != "" {
			return styleErrored.Render("error: "+m.lastError) + "\n\n" + styleMuted.Render("(daemon unreachable?)")
		}
		return styleMuted.Render("no islands yet\n\n`q` to quit, then `dejima init --repo <url>`")
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render("Islands"))
	b.WriteString("\n\n")
	for i, isl := range m.islands {
		glyph := glyphFor(isl)
		row := fmt.Sprintf("%s  %-16s  %s", glyph, truncate(isl.Name, 16), shortStatus(isl, m.dirtyOps[isl.Name]))
		if i == m.selected {
			row = styleSelected.Render("▶ " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderDetail(_ int) string {
	if m.detail == nil {
		if name := m.selectedName(); name != "" {
			return styleMuted.Render("loading " + name + "…")
		}
		return styleMuted.Render("select an island")
	}
	d := m.detail
	var b strings.Builder
	b.WriteString(styleTitle.Render(d.Name))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("repo:      %s\n", styleAccent.Render(truncate(d.Repo, 60))))
	b.WriteString(fmt.Sprintf("agent:     %s\n", styleAccent.Render(d.Agent)))
	b.WriteString(fmt.Sprintf("state:     %s\n", coloredStateText(d)))
	if d.Stats != nil {
		b.WriteString(fmt.Sprintf("memory:    %s / %s\n",
			humanBytes(d.Stats.MemoryUsageBytes), humanBytes(d.Stats.MemoryLimitBytes)))
		b.WriteString(fmt.Sprintf("cpu:       %.1f%%\n", d.Stats.CPUPercent))
	}
	if d.AgentState != nil {
		b.WriteString(fmt.Sprintf("agent:     %s (%s ago)\n",
			d.AgentState.Latest, time.Since(d.AgentState.UpdatedAt).Round(time.Second)))
	}
	if d.Git != nil {
		clean := "dirty"
		if d.Git.Clean {
			clean = "clean"
		}
		s := fmt.Sprintf("git:       %s · %s", d.Git.Branch, clean)
		if !d.Git.Clean {
			s += fmt.Sprintf(" (%d files)", d.Git.DirtyFiles)
		}
		if d.Git.Ahead > 0 {
			s += fmt.Sprintf(" · %d ahead", d.Git.Ahead)
		}
		if d.Git.Behind > 0 {
			s += fmt.Sprintf(" · %d behind", d.Git.Behind)
		}
		b.WriteString(s + "\n")
	}
	if len(d.Attached) > 0 {
		labels := make([]string, 0, len(d.Attached))
		for _, a := range d.Attached {
			labels = append(labels, a.Label)
		}
		b.WriteString("attached:  " + strings.Join(labels, ", ") + "\n")
	}
	// Crash health — only worth showing when something's wrong.
	if h := d.Health; h != nil && (h.OOMKilled || h.RestartCount > 0) {
		var warn string
		if h.OOMKilled {
			warn = "OOM-killed (hit memory cap)"
		}
		if h.RestartCount > 0 {
			if warn != "" {
				warn += " · "
			}
			warn += fmt.Sprintf("%d restart(s)", h.RestartCount)
		}
		b.WriteString("health:    " + styleErrored.Render(warn) + "\n")
	}

	if len(m.events_) > 0 {
		b.WriteString("\n")
		b.WriteString(styleHeader.Render("Recent"))
		b.WriteString("\n")
		max := 6
		if len(m.events_) < max {
			max = len(m.events_)
		}
		for _, e := range m.events_[:max] {
			b.WriteString(fmt.Sprintf("  %s  %s\n",
				styleMuted.Render(timeAgo(e.Timestamp)), string(e.Type)))
		}
	}
	return b.String()
}

func (m tuiModel) renderFooter() string {
	// Two key lines, right-aligned to a shared edge: global commands on top,
	// island lifecycle below. The substrate-health strip keeps the left of
	// the first line.
	keys1 := "[n] new   [⏎] open   [s] server   [?] help   [q] quit"
	keys2 := "[a] attach here   [h] hibernate   [w] wake   [r] reset   [d] purge"
	left := m.renderFooterLeft()
	pad1 := m.width - lipgloss.Width(left) - lipgloss.Width(keys1) - 2
	if pad1 < 1 {
		pad1 = 1
	}
	pad2 := m.width - lipgloss.Width(keys2) - 2
	if pad2 < 1 {
		pad2 = 1
	}
	return " " + left + strings.Repeat(" ", pad1) + styleFooter.Render(keys1) + "\n" +
		strings.Repeat(" ", pad2) + styleFooter.Render(keys2) + " "
}

// renderFooterLeft assembles the substrate-health strip + island totals (or
// the last error if the daemon has gone unreachable).
func (m tuiModel) renderFooterLeft() string {
	if m.lastError != "" {
		return styleErrored.Render("⚠ " + truncate(m.lastError, 60))
	}
	o := m.overview
	if o == nil {
		return styleMuted.Render("loading…")
	}
	dockerGlyph := healthGlyph(o.DockerReachable)
	imagePart := healthGlyph(o.IslandImagePresent) + " " + styleMuted.Render("image")
	switch {
	case m.building:
		imagePart = styleWaiting.Render("⏳ image building…")
	case !o.IslandImagePresent:
		imagePart = styleWaiting.Render("✗ image missing — press b to build")
	}
	parts := []string{
		dockerGlyph + " " + styleMuted.Render("docker"),
		imagePart,
		styleMuted.Render(fmt.Sprintf("%d islands · %d running · %d hibernated",
			o.TotalIslands, o.Running, o.Hibernated)),
	}
	if o.MemoryUsageBytes > 0 {
		parts = append(parts, styleMuted.Render(humanBytes(o.MemoryUsageBytes)))
	}
	if o.WebhookCount > 0 {
		parts = append(parts, styleMuted.Render(fmt.Sprintf("%d webhook(s)", o.WebhookCount)))
	}
	if m.skew != "" {
		parts = append(parts, styleWaiting.Render("⚠ "+truncate(m.skew, 70)))
	}
	return strings.Join(parts, styleMuted.Render(" · "))
}

// renderHelp draws the help overlay: a Basic Usage section always, and an
// expandable Advanced section toggled with `a`.
func (m tuiModel) renderHelp() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Dejima — how to use it"))
	b.WriteString("\n\n")

	b.WriteString(styleHeader.Render("Basic usage"))
	b.WriteString("\n")
	basic := [][2]string{
		{"n", "new island — pick a repo (or paste a URL), choose an agent, launch"},
		{"⏎", "open the highlighted island in a new window — dashboard stays up"},
		{"a", "attach here instead — replaces the dashboard with the agent"},
		{"↑/↓ j/k", "move between islands   ·   g/G jump to top/bottom"},
		{"Ctrl-b d", "detach from a session — the agent keeps running inside"},
		{"q", "quit the dashboard"},
	}
	for _, kv := range basic {
		b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(fmt.Sprintf("%-9s", kv[0])), styleMuted.Render(kv[1])))
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("An island = one agent in a contained workspace. The same repo can back\nseveral islands (e.g. claude-code and codex, or two agents on parallel tasks)."))
	b.WriteString("\n\n")

	if !m.helpAdvanced {
		b.WriteString(styleAccent.Render("[a]") + styleMuted.Render(" show advanced commands ▾    ") + styleAccent.Render("[?/esc]") + styleMuted.Render(" close"))
		return b.String()
	}

	b.WriteString(styleHeader.Render("Manage (single-key, on the highlighted island)"))
	b.WriteString("\n")
	manage := [][2]string{
		{"h", "hibernate — stop the container, keep all data"},
		{"w", "wake a hibernated island"},
		{"r", "reset agent state (workspace preserved) — confirms first"},
		{"u", "upgrade — recreate on the current island image, all state kept — confirms first"},
		{"b", "build the island image on the daemon host — confirms first"},
		{"d", "purge — destroy the island and its volumes — confirms first"},
		{"s", "switch connection target (local / saved remote daemons)"},
		{"R", "refresh now"},
	}
	for _, kv := range manage {
		b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(fmt.Sprintf("%-9s", kv[0])), styleMuted.Render(kv[1])))
	}

	b.WriteString("\n")
	b.WriteString(styleHeader.Render("From the shell (scriptable; the TUI is just a front-end)"))
	b.WriteString("\n")
	shell := [][2]string{
		{"dejima init --repo <url|path>", "provision an island (--local-copy to seed unpushed work)"},
		{"dejima connect <name>", "attach a terminal into an island"},
		{"dejima ls / status <name>", "list islands / detail view"},
		{"dejima exec <name> -- <cmd>", "run a one-shot command inside an island"},
		{"dejima cp <src> <dst>", "copy files in or out"},
		{"dejima logs <name>", "tail an island's container logs"},
		{"dejima hibernate|wake|reset|purge", "lifecycle from the CLI"},
		{"dejima image build / upgrade <name>", "rebuild the island image / roll an island onto it"},
		{"dejima auth push / status", "send this machine's Claude login to the daemon host"},
		{"DEJIMA_HOST=host:7273 dejima …", "drive a remote daemon over your tailnet"},
	}
	for _, kv := range shell {
		b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(fmt.Sprintf("%-32s", kv[0])), styleMuted.Render(kv[1])))
	}

	b.WriteString("\n")
	b.WriteString(styleAccent.Render("[a]") + styleMuted.Render(" hide advanced ▴    ") + styleAccent.Render("[?/esc]") + styleMuted.Render(" close"))
	return b.String()
}

// healthGlyph picks the right colored bullet for a boolean health signal.
func healthGlyph(ok bool) string {
	if ok {
		return styleRunning.Render("●")
	}
	return styleErrored.Render("✗")
}

func (m tuiModel) renderConfirm() string {
	c := m.confirm
	var prompt string
	switch c.verb {
	case "reset":
		prompt = fmt.Sprintf("Clear agent state for %q (workspace preserved)? Type 'y' and press Enter: %s",
			c.island, c.answer)
	case "upgrade":
		prompt = fmt.Sprintf("Recreate %q on the current island image (all state preserved)? Type 'y' and press Enter: %s",
			c.island, c.answer)
	case "build-image":
		prompt = fmt.Sprintf("Rebuild the island image? Takes a few minutes; islands pick it up on upgrade. Type 'y' and press Enter: %s",
			c.answer)
	case "purge":
		prompt = fmt.Sprintf("DESTROY %q (including all volumes). Type the island name to confirm: %s",
			c.island, c.answer)
	}
	return styleErrored.Render("┌── ") + prompt + styleErrored.Render(" ──┐")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func glyphFor(isl api.IslandInfo) string {
	if isl.AgentState != nil && isl.AgentState.Latest == "waiting-for-input" {
		return styleWaiting.Render("!")
	}
	switch isl.Container {
	case "running":
		return styleRunning.Render("●")
	case "exited", "stopped", "paused", "created":
		return styleHibernate.Render("⏸")
	case "missing":
		return styleErrored.Render("◌")
	default:
		return styleErrored.Render("✱")
	}
}

func shortStatus(isl api.IslandInfo, transient string) string {
	if transient != "" {
		return styleAccent.Render(transient + "…")
	}
	parts := []string{isl.Container}
	if isl.Stats != nil && isl.Container == "running" {
		parts = append(parts, fmt.Sprintf("%s · %.0f%%", humanBytes(isl.Stats.MemoryUsageBytes), isl.Stats.CPUPercent))
	}
	parts = append(parts, isl.Agent)
	return strings.Join(parts, " · ")
}

func coloredStateText(isl *api.IslandInfo) string {
	switch isl.Container {
	case "running":
		return styleRunning.Render("running")
	case "exited", "stopped":
		return styleHibernate.Render("hibernated")
	case "missing":
		return styleErrored.Render("missing")
	default:
		return isl.Container
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 4 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func timeAgo(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// _ = exec to avoid an unused-import error if we change the connect path later.
var _ = exec.Command
