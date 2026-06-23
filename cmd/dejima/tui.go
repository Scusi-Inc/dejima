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
		return runConnectFromTUI(ctx, final.client, final.connectTo, final.connectAgent)
	}
	return nil
}

func runConnectFromTUI(ctx context.Context, c *api.Client, name, agentID string) error {
	info, err := c.GetIsland(ctx, name)
	if err != nil {
		return err
	}
	if info.Container != "running" {
		return fmt.Errorf("island %q is %s; `dejima wake %s` first", name, info.Container, name)
	}
	return runSession(ctx, c, name, agentID, defaultLabel())
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

	selected     int
	width        int
	height       int
	lastError    string
	connectTo    string // set on quit-to-connect; main() acts on this
	connectAgent string // agent id to attach to alongside connectTo ("" = primary)
	confirm      *confirmPrompt
	dirtyOps     map[string]string // name → "hibernating" etc. (transient hint)
	building     bool              // island image build in flight

	help         bool            // help overlay visible
	helpAdvanced bool            // advanced section of the help overlay expanded
	creator      *creatorModel   // non-nil while the new-island flow is active
	switcher     *switcherModel  // non-nil while the connection switcher is open
	agentAdder   *agentAdder     // non-nil while the add-agent flow is active
	expanded     map[string]bool // island name → agents-revealed (default: all expanded)

	activeHost  string // current target: "" = local socket, else host:port
	activeLabel string // profile name for the active target, if known
	skew        string // client/daemon version-skew warning, or ""
}

type confirmPrompt struct {
	verb   string // "purge", "reset", "remove-agent"
	island string
	agent  string // for "remove-agent"
	answer string
}

func initialTUIModel(c *api.Client) tuiModel {
	return tuiModel{
		client:     c,
		dirtyOps:   map[string]string{},
		expanded:   map[string]bool{},
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
	if name == "" {
		return nil // e.g. the trailing "+ new island" row has no island
	}
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
		if n := m.rowCount(); m.selected >= n {
			m.selected = n - 1
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

	case agentAddedMsg:
		if m.agentAdder != nil {
			if msg.err != nil {
				m.agentAdder.adding = false
				m.agentAdder.err = msg.err.Error()
				return m, nil
			}
			m.agentAdder = nil
		}
		m.expanded[msg.island] = true // reveal the island so the new agent shows
		return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())

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
	// The add-agent overlay owns keys while open.
	if m.agentAdder != nil {
		return m.agentAdderKey(msg)
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
		if m.selected < m.rowCount()-1 {
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
		m.selected = m.rowCount() - 1
		return m, m.fetchDetailCmd(m.selectedName())
	case "enter", "o":
		// Dispatch on the row kind: the affordance rows open the creator /
		// add-agent flow; island and agent rows open a workspace (or, for a
		// headless agent, its logs).
		return m.activateRow()
	case " ", "right", "left":
		// Expand/collapse an island to reveal its agents + the add-agent row.
		if r := m.currentRow(); r.kind == rowIsland {
			switch msg.String() {
			case "right":
				m.expanded[r.island] = true
			case "left":
				m.expanded[r.island] = false
			default:
				m.expanded[r.island] = !m.islandExpandedByName(r.island)
			}
		}
		return m, nil
	case "E":
		// Expand/collapse all islands at once. Flips on the current state: if
		// every island is already expanded, collapse them all; otherwise expand.
		expand := !m.allIslandsExpanded()
		for _, isl := range m.islands {
			m.expanded[isl.Name] = expand
		}
		return m, nil
	case "a":
		// Explicit "attach in this terminal" — replaces the dashboard. Useful
		// when the user actually wants the old behavior even though a
		// new-window backend is available.
		if r := m.currentRow(); (r.kind == rowIsland || r.kind == rowAgent) && m.detail != nil && m.detail.Container == "running" {
			if m.isHeadlessAgent(r.island, r.agentID) {
				m.lastError = "headless agent — press ⏎ to view its logs"
				return m, nil
			}
			m.connectTo, m.connectAgent = r.island, r.agentID
			return m, tea.Quit
		}
	case "+":
		// Add an agent to the current island (pick type; headless prompts a cmd).
		if r := m.currentRow(); r.island != "" {
			return m.openAgentAdder(r.island)
		}
	case "X":
		// Remove the selected agent (agent rows only; never the last one).
		if r := m.currentRow(); r.kind == rowAgent {
			if isl, ok := m.islandByName(r.island); ok && len(isl.Agents) <= 1 {
				m.lastError = "can't remove the only agent — purge the island instead"
				return m, nil
			}
			m.confirm = &confirmPrompt{verb: "remove-agent", island: r.island, answer: "", agent: r.agentID}
		}
	case "e":
		// Rename: island rows set a cosmetic display title; agent rows relabel.
		// Both are cosmetic — the island Name slug and the agent id are unchanged.
		switch r := m.currentRow(); r.kind {
		case rowAgent:
			cur := ""
			if isl, ok := m.islandByName(r.island); ok {
				cur = agentByID(isl, r.agentID).Label
			}
			m.confirm = &confirmPrompt{verb: "relabel-agent", island: r.island, agent: r.agentID, answer: cur}
		case rowIsland:
			cur := ""
			if isl, ok := m.islandByName(r.island); ok {
				cur = isl.Title
			}
			m.confirm = &confirmPrompt{verb: "rename-island", island: r.island, answer: cur}
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
	case "remove-agent":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			m.dirtyOps[c.island] = "removing agent"
			return m, m.removeAgentCmd(c.island, c.agent)
		}
	case "relabel-agent":
		// The typed text is the new label (blank clears it); no y/n gate.
		m.dirtyOps[c.island] = "renaming agent"
		return m, m.relabelAgentCmd(c.island, c.agent, strings.TrimSpace(c.answer))
	case "rename-island":
		// The typed text is the new display title (blank resets to the name).
		m.dirtyOps[c.island] = "renaming"
		return m, m.setIslandTitleCmd(c.island, strings.TrimSpace(c.answer))
	}
	return m, nil
}

// setIslandTitleCmd sets an island's cosmetic display title (blank resets it).
func (m tuiModel) setIslandTitleCmd(name, title string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := m.client.SetIslandTitle(ctx, name, title)
		return opCompleteMsg{name: name, verb: "rename-island", err: err}
	}
}

// relabelAgentCmd renames (relabels) an agent. An empty label clears it.
func (m tuiModel) relabelAgentCmd(name, agentID, label string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := m.client.RelabelAgent(ctx, name, agentID, label)
		return opCompleteMsg{name: name, verb: "relabel-agent", err: err}
	}
}

// removeAgentCmd removes an agent from an island.
func (m tuiModel) removeAgentCmd(name, agentID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := m.client.RemoveAgent(ctx, name, agentID)
		return opCompleteMsg{name: name, verb: "remove-agent", err: err}
	}
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

// rowKind tags each visible line in the island→agent tree.
type rowKind int

const (
	rowIsland    rowKind = iota // an island header (also the primary, when collapsed)
	rowAgent                    // an agent under an expanded island
	rowAddAgent                 // the "+ add agent" affordance under an expanded island
	rowNewIsland                // the trailing "+ new island" affordance
)

// treeRow is one visible line in the two-level island→agent list.
type treeRow struct {
	kind    rowKind
	island  string
	agentID string
}

// islandExpanded reports whether an island's agents are revealed. Multi-agent
// islands default to expanded. Either can be toggled (space / ←/→), or all at
// once (E), which is remembered in m.expanded.
func (m tuiModel) islandExpanded(isl api.IslandInfo) bool {
	if v, ok := m.expanded[isl.Name]; ok {
		return v
	}
	return true
}

// allIslandsExpanded reports whether every island is currently expanded — used
// to flip the expand/collapse-all action and its footer label.
func (m tuiModel) allIslandsExpanded() bool {
	for _, isl := range m.islands {
		if !m.islandExpanded(isl) {
			return false
		}
	}
	return true
}

func (m tuiModel) islandExpandedByName(name string) bool {
	if isl, ok := m.islandByName(name); ok {
		return m.islandExpanded(isl)
	}
	return false
}

// visibleRows flattens the sorted islands into the navigable row list: each
// island header, its agents + an add-agent row when expanded, and one trailing
// new-island row.
func (m tuiModel) visibleRows() []treeRow {
	rows := make([]treeRow, 0, len(m.islands)+1)
	for _, isl := range m.islands {
		rows = append(rows, treeRow{kind: rowIsland, island: isl.Name})
		if m.islandExpanded(isl) {
			for _, a := range isl.Agents {
				rows = append(rows, treeRow{kind: rowAgent, island: isl.Name, agentID: a.ID})
			}
			rows = append(rows, treeRow{kind: rowAddAgent, island: isl.Name})
		}
	}
	rows = append(rows, treeRow{kind: rowNewIsland})
	return rows
}

// islandDisplay is the user-facing island name: its Title if set, else the slug.
func islandDisplay(isl api.IslandInfo) string {
	if isl.Title != "" {
		return isl.Title
	}
	return isl.Name
}

// islandByName finds a loaded island by name.
func (m tuiModel) islandByName(name string) (api.IslandInfo, bool) {
	for _, isl := range m.islands {
		if isl.Name == name {
			return isl, true
		}
	}
	return api.IslandInfo{}, false
}

// isHeadlessAgent reports whether the given agent (or, for agentID=="", the
// island's primary agent) has no attach surface and should open logs instead.
func (m tuiModel) isHeadlessAgent(island, agentID string) bool {
	isl, ok := m.islandByName(island)
	if !ok {
		return false
	}
	if agentID == "" {
		if len(isl.Agents) > 0 {
			return !isl.Agents[0].Attachable
		}
		return isl.Agent == api.AgentHeadless // older daemon: no Agents list
	}
	for _, a := range isl.Agents {
		if a.ID == agentID {
			return !a.Attachable
		}
	}
	return false
}

// activateRow handles Enter/o on the selected row: affordance rows open the
// creator or add-agent flow; island and agent rows open a workspace, except
// headless agents, which open their logs (they have no attach surface).
func (m tuiModel) activateRow() (tea.Model, tea.Cmd) {
	row := m.currentRow()
	switch row.kind {
	case rowNewIsland:
		return m.openCreator()
	case rowAddAgent:
		return m.openAgentAdder(row.island)
	}
	name := row.island
	if name == "" {
		return m, nil
	}
	if m.detail != nil && m.detail.Container != "running" {
		m.lastError = fmt.Sprintf("island %q is %s; `w` to wake it first", name, m.detail.Container)
		return m, nil
	}
	if m.isHeadlessAgent(name, row.agentID) {
		return m.openAgentLogs(name, row.agentID)
	}
	if canOpenNewWindow() {
		if err := m.openInNewWindow(name, row.agentID); err != nil {
			m.lastError = err.Error()
		}
		return m, nil
	}
	m.connectTo, m.connectAgent = name, row.agentID
	return m, tea.Quit
}

// openAgentLogs opens a headless agent's logs in a new window, or points the
// user at the CLI when no new-window backend is available.
func (m tuiModel) openAgentLogs(name, agentID string) (tea.Model, tea.Cmd) {
	if canOpenNewWindow() {
		if err := m.openAgentLogsWindow(name, agentID); err != nil {
			m.lastError = err.Error()
		}
		return m, nil
	}
	hint := "dejima logs " + name + " --follow"
	if agentID != "" {
		hint = "dejima logs " + name + " --agent " + agentID + " --follow"
	}
	m.lastError = "headless agent — `" + hint + "`"
	return m, nil
}

// currentRow returns the selected tree row, or a zero row if none.
func (m tuiModel) currentRow() treeRow {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return treeRow{}
	}
	return rows[m.selected]
}

// selectedName is the island of the current row (header or agent), used for
// island-level operations and detail fetching.
func (m tuiModel) selectedName() string {
	return m.currentRow().island
}

// selectedAgent is the agent id of the current row, or "" when an island header
// (or single-agent island) is selected — meaning the primary agent.
func (m tuiModel) selectedAgent() string {
	return m.currentRow().agentID
}

// rowCount is the number of navigable rows.
func (m tuiModel) rowCount() int {
	return len(m.visibleRows())
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
	if m.agentAdder != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.agentAdder.view())
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
	// -5 = 3 footer lines (health strip + two key-hint rows) + the body pane's
	// top/bottom border.
	bodyHeight := m.height - headerHeight - 5
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

	byName := make(map[string]api.IslandInfo, len(m.islands))
	for _, isl := range m.islands {
		byName[isl.Name] = isl
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render("Islands"))
	b.WriteString("\n\n")
	for i, row := range m.visibleRows() {
		var line string
		switch row.kind {
		case rowNewIsland:
			line = styleAccent.Render("+ new island")
		case rowAddAgent:
			line = "   " + styleMuted.Render("+ add agent")
		case rowAgent:
			a := agentByID(byName[row.island], row.agentID)
			line = "   └ " + agentRowText(a)
		default: // rowIsland
			isl, ok := byName[row.island]
			if !ok {
				continue
			}
			caret := "▸"
			if m.islandExpanded(isl) {
				caret = "▾"
			}
			label := truncate(islandDisplay(isl), 16)
			if len(isl.Agents) > 1 {
				label = truncate(islandDisplay(isl), 12) + fmt.Sprintf(" (%d)", len(isl.Agents))
			}
			line = fmt.Sprintf("%s %s  %-16s  %s", caret, glyphFor(isl), label, shortStatus(isl, m.dirtyOps[isl.Name]))
		}
		if i == m.selected {
			line = styleSelected.Render("▶ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// agentByID finds an agent within an island, or returns a zero value.
func agentByID(isl api.IslandInfo, id string) api.AgentInfo {
	for _, a := range isl.Agents {
		if a.ID == id {
			return a
		}
	}
	return api.AgentInfo{ID: id}
}

// Agent kind glyphs. Shape encodes what the agent *is* (stable identity);
// color (see agentGlyph) encodes how it's *doing* (state). Islands keep their
// own lifecycle glyphs (glyphFor) — these are for the indented agent rows.
const (
	glyphTerminal = "❯" // interactive agent — owns a tmux session, attachable
	glyphHeadless = "■" // headless agent — supervised background process, logs only
)

// agentGlyph renders an agent's kind glyph colored by its state: the shape says
// terminal vs headless, the color says running / idle / needs-you / error.
func agentGlyph(a api.AgentInfo) string {
	g := glyphTerminal
	if !a.Attachable {
		g = glyphHeadless
	}
	style := styleHibernate // gray: idle / stopped (also the default)
	switch {
	case a.Error != "":
		style = styleErrored
	case a.AgentState != nil && a.AgentState.Latest == "waiting-for-input":
		style = styleWaiting
	case a.State == "running":
		style = styleRunning
	}
	return style.Render(g)
}

// agentDisplayName is the human-facing name for an agent: its user-given label
// if set, else its type ("Claude Code", "headless", …). The id (a1/a2/…) is a
// separate addressing handle — meaningful but terse — so it rides along muted
// rather than leading the display. See [agentRowText] / renderAgentDetail.
func agentDisplayName(a api.AgentInfo) string {
	if a.Label != "" {
		return a.Label
	}
	return a.Type
}

// agentRowText renders one agent's list line: state-colored kind glyph, name
// (label/type), the agent's type, the muted id handle, then the latest signal.
// The type column lines up with the island rows' status column (offset 23). A
// label-less agent's name already *is* its type, so the type column is left
// blank rather than repeating it.
func agentRowText(a api.AgentInfo) string {
	typ := a.Type
	if a.Label == "" {
		typ = ""
	}
	sig := ""
	if a.Error != "" {
		sig = "  " + styleErrored.Render("error")
	} else if a.AgentState != nil && a.AgentState.Latest != "" {
		sig = "  " + a.AgentState.Latest
	}
	return fmt.Sprintf("%s %-14s  %s  %s%s",
		agentGlyph(a),
		truncate(agentDisplayName(a), 14),
		styleMuted.Render(fmt.Sprintf("%-11s", typ)),
		styleMuted.Render("·"+a.ID),
		sig)
}

func (m tuiModel) renderDetail(_ int) string {
	// The trailing "+ new island" row has no island behind it.
	if m.currentRow().kind == rowNewIsland {
		return styleTitle.Render("+ New island") + "\n\n" +
			styleMuted.Render("Press ⏎ to pick a repo and an agent, then launch.")
	}
	if m.currentRow().kind == rowAddAgent {
		return styleTitle.Render("+ Add agent") + "\n\n" +
			styleMuted.Render("Press ⏎ to add an agent to "+styleAccent.Render(m.selectedName())+styleMuted.Render(".\nClaude Code, Codex, or a headless background command."))
	}
	if m.detail == nil {
		if name := m.selectedName(); name != "" {
			return styleMuted.Render("loading " + name + "…")
		}
		return styleMuted.Render("select an island")
	}
	d := m.detail
	// When an agent row is selected, show that agent's focused detail.
	if agentID := m.selectedAgent(); agentID != "" {
		return m.renderAgentDetail(d, agentID)
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render(islandDisplay(*d)))
	b.WriteString("\n\n")
	// Show the addressable slug when a title is masking it (CLI addresses by slug).
	if d.Title != "" && d.Title != d.Name {
		b.WriteString(fmt.Sprintf("name:      %s\n", styleMuted.Render(d.Name)))
	}
	b.WriteString(fmt.Sprintf("repo:      %s\n", styleAccent.Render(truncate(d.Repo, 60))))
	if len(d.Agents) > 1 {
		b.WriteString(fmt.Sprintf("agents:    %s\n", styleAccent.Render(fmt.Sprintf("%d", len(d.Agents)))))
	} else {
		b.WriteString(fmt.Sprintf("agent:     %s\n", styleAccent.Render(d.Agent)))
	}
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

	if len(d.Agents) > 1 {
		b.WriteString("\n")
		b.WriteString(styleHeader.Render("Agents"))
		b.WriteString("\n")
		for _, a := range d.Agents {
			b.WriteString("  " + agentRowText(a) + "\n")
		}
		b.WriteString(styleMuted.Render("  [+] add   [X] remove (on an agent)") + "\n")
	}

	renderRecent(&b, m.events_)
	return b.String()
}

// renderAgentDetail shows one agent's focused view within its island.
func (m tuiModel) renderAgentDetail(d *api.IslandInfo, agentID string) string {
	a := agentByID(*d, agentID)
	var b strings.Builder
	b.WriteString(styleTitle.Render(d.Name + " / " + agentDisplayName(a)))
	b.WriteString("\n\n")
	// id is the addressing handle (connect island/a2, branch agent/a2, worktree
	// .agents/a2); the label/type leads in the title above.
	b.WriteString(fmt.Sprintf("id:        %s\n", styleMuted.Render(a.ID)))
	b.WriteString(fmt.Sprintf("type:      %s\n", styleAccent.Render(a.Type)))
	kind := "terminal — attachable"
	if !a.Attachable {
		kind = "headless — background process, logs only"
	}
	b.WriteString(fmt.Sprintf("kind:      %s %s\n", agentGlyph(a), styleMuted.Render(kind)))
	state := a.State
	if state == "" {
		state = "—"
	}
	b.WriteString(fmt.Sprintf("session:   %s\n", state))
	if !a.CreatedAt.IsZero() {
		b.WriteString(fmt.Sprintf("uptime:    %s\n", humanDuration(time.Since(a.CreatedAt))))
	}
	if a.Branch != "" {
		b.WriteString(fmt.Sprintf("branch:    %s\n", a.Branch))
	}
	if a.Worktree != "" {
		b.WriteString(fmt.Sprintf("worktree:  %s\n", truncate(a.Worktree, 50)))
	}
	if a.AgentState != nil && a.AgentState.Latest != "" {
		b.WriteString(fmt.Sprintf("activity:  %s (%s ago)\n",
			a.AgentState.Latest, time.Since(a.AgentState.UpdatedAt).Round(time.Second)))
	}
	if a.Error != "" {
		b.WriteString("error:     " + styleErrored.Render(truncate(a.Error, 50)) + "\n")
	}
	if len(a.Attached) > 0 {
		// Per agent we show who's attached and for how long (island view omits the
		// duration); JoinedAt comes from the presence snapshot.
		parts := make([]string, 0, len(a.Attached))
		for _, c := range a.Attached {
			parts = append(parts, fmt.Sprintf("%s (%s)", c.Label, timeAgo(c.JoinedAt)))
		}
		b.WriteString("attached:  " + strings.Join(parts, ", ") + "\n")
	}
	// Recent timeline for just this agent, filtered from the island event log.
	renderRecent(&b, eventsForAgent(m.events_, a.ID))
	b.WriteString("\n")
	open := "[⏎] attach"
	if !a.Attachable {
		open = "[⏎] logs (headless — no attach surface)"
	}
	b.WriteString(styleMuted.Render(open + "   [X] remove   [+] add another"))
	return b.String()
}

func (m tuiModel) renderFooter() string {
	// Substrate-health strip on its own line, then two key-hint lines below it
	// (global commands, then island lifecycle), right-aligned to a shared edge.
	// The strip used to share line one with the global commands, which collided
	// on narrow terminals — giving it its own row keeps both readable.
	expandAll := "[E] expand all"
	if m.allIslandsExpanded() {
		expandAll = "[E] collapse all"
	}
	keys1 := "[n] new   [⏎] open   [space] expand   " + expandAll + "   [+] add agent   [s] server   [?] help   [q] quit"
	keys2 := "[a] attach here   [e] rename   [X] rm agent   [h] hibernate   [w] wake   [r] reset   [d] purge"
	left := m.renderFooterLeft()
	pad1 := m.width - lipgloss.Width(keys1) - 2
	if pad1 < 1 {
		pad1 = 1
	}
	pad2 := m.width - lipgloss.Width(keys2) - 2
	if pad2 < 1 {
		pad2 = 1
	}
	return " " + left + "\n" +
		strings.Repeat(" ", pad1) + styleFooter.Render(keys1) + "\n" +
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
		{"⏎", "open the highlighted row — island/agent in a new window, or run the affordance"},
		{"space ←/→", "expand an island to its agents, the + add-agent row, and headless logs"},
		{"E", "expand / collapse all islands at once (flips on the current state)"},
		{"+", "add an agent — Claude Code, Codex, or a headless background command"},
		{"e", "rename — island display title, or relabel an agent (cosmetic; the slug/id stay)"},
		{"a", "attach here instead — replaces the dashboard with the agent"},
		{"↑/↓ j/k", "move between rows   ·   g/G jump to top/bottom"},
		{"Ctrl-b d", "detach from a session — the agent keeps running inside"},
		{"q", "quit the dashboard"},
	}
	for _, kv := range basic {
		b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(fmt.Sprintf("%-9s", kv[0])), styleMuted.Render(kv[1])))
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("An island = a contained workspace that can hold several agents sharing its\ncreds and git. Expand one with [space], then [+] add agents (interactive or a\nheadless background command). Headless agents have no screen — ⏎ opens their logs."))
	b.WriteString("\n\n")
	b.WriteString(styleHeader.Render("Glyphs"))
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render(fmt.Sprintf(
		"%s island   %s terminal agent   %s headless agent", "●", glyphTerminal, glyphHeadless)))
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render("color = state: ") +
		styleRunning.Render("running") + styleMuted.Render(" · ") +
		styleHibernate.Render("idle") + styleMuted.Render(" · ") +
		styleWaiting.Render("needs you") + styleMuted.Render(" · ") +
		styleErrored.Render("error"))
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
	case "relabel-agent":
		prompt = fmt.Sprintf("Rename agent %s (blank clears the label). Type a name and press Enter: %s",
			c.agent, c.answer)
	case "rename-island":
		prompt = fmt.Sprintf("Rename %q (display title; blank resets to the name). Type a title and press Enter: %s",
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
	// Per-agent type belongs on each agent row, not here — an island's first
	// agent's type says nothing about the rest. (See agentRowText.)
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

// humanDuration formats an elapsed span as up to two units ("1h 14m", "3m 02s",
// "2d 4h") — coarser than timeAgo but more legible for an uptime field.
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}

// eventsForAgent narrows the island event log to one agent's events, preserving
// order. Island-scoped events (empty Agent) are excluded.
func eventsForAgent(evs []events.Event, agentID string) []events.Event {
	out := make([]events.Event, 0, len(evs))
	for _, e := range evs {
		if e.Agent == agentID {
			out = append(out, e)
		}
	}
	return out
}

// renderRecent appends a "Recent" section listing up to six of the given events
// (assumed newest-first, as the API returns them). No-op when evs is empty.
// Shared by the island and per-agent detail panels.
func renderRecent(b *strings.Builder, evs []events.Event) {
	if len(evs) == 0 {
		return
	}
	b.WriteString("\n")
	b.WriteString(styleHeader.Render("Recent"))
	b.WriteString("\n")
	n := 6
	if len(evs) < n {
		n = len(evs)
	}
	for _, e := range evs[:n] {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			styleMuted.Render(timeAgo(e.Timestamp)), string(e.Type)))
	}
}

// _ = exec to avoid an unused-import error if we change the connect path later.
var _ = exec.Command
