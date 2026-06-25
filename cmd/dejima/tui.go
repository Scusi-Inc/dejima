package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
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
	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/hostterm"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/policy"
	"github.com/aoos/dejima/internal/selfupdate"
	"github.com/aoos/dejima/internal/version"
	"github.com/aoos/dejima/internal/vmmem"
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
	// The dashboard runs in a loop with attached sessions: normally an attach
	// quits the TUI into the raw bridge and a detach exits to the shell, but the
	// summon chord (Ctrl-\) ends a session with errSummonBand, which brings us
	// back here with the host-terminal band open instead of exiting.
	summonReturn := false
	for {
		m := initialTUIModel(c)
		if summonReturn {
			m.bandExpanded, m.bandFocused = true, true
		}
		finalRaw, rerr := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
		if rerr != nil {
			return rerr
		}
		final := finalRaw.(tuiModel)
		c = final.client // the switcher may have swapped the client mid-session

		var sessErr error
		switch {
		case final.connectShell != "":
			// A contained shell at the island (/workspace inside its container).
			sessErr = runInShellSession(ctx, c, final.connectShell, defaultLabel(), true)
		case final.connectTerminal != "":
			// Attach to a host terminal (uncontained shell on the daemon host).
			sessErr = runTerminalSession(ctx, c, final.connectTerminal, defaultLabel(), true)
		case final.connectTo != "":
			sessErr = runConnectFromTUI(ctx, c, final.connectTo, final.connectAgent)
		default:
			return nil // the user quit the dashboard — leave dejima
		}
		if errors.Is(sessErr, errSummonBand) {
			summonReturn = true
			continue // re-enter the dashboard with the band open
		}
		return sessErr // normal detach / error → exit to the shell, as before
	}
}

func runConnectFromTUI(ctx context.Context, c *api.Client, name, agentID string) error {
	info, err := c.GetIsland(ctx, name)
	if err != nil {
		return err
	}
	if info.Container != "running" {
		return fmt.Errorf("island %q is %s; `dejima wake %s` first", name, info.Container, name)
	}
	// Launched from the TUI, so the summon chord (Ctrl-\) can return there.
	return runSessionSummonable(ctx, c, name, agentID, defaultLabel())
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

	// Setup-readiness snapshot (fetched once at Init) so the UI can warn about a
	// missing credential BEFORE an island is created rather than at first agent
	// attach. setupChecked guards against a false warning before the fetch lands.
	setupChecked bool
	claudeSeeded bool            // daemon can seed new islands with Claude creds
	agentKeyGap  map[string]bool // agent type → requires an LLM provider key, none configured for it

	selected  int
	grouped   bool // group the island list by repo (toggled with `p`)
	width     int
	height    int
	lastError string
	// daemonHelp, when non-nil, is an actionable diagnosis of a *local*
	// daemon-unreachable failure (why dejimad isn't up + how to fix it). Computed
	// once when the connection error arrives — service.Detect() shells out, so it
	// must not run per render — and cleared the moment a list load succeeds.
	daemonHelp     *daemonDiagnosis
	lastNotice     string // transient success hint (e.g. ssh setup); shown until replaced
	sshHost        string // resolved SSH-façade host (cached from overview; see overviewMsg)
	sshPort        string // resolved SSH-façade port
	sshResolvedFor string // the SSHAddr we last resolved, so we don't re-exec tailscale each frame
	latestRelease  string // newest published release tag (from GitHub; "" until fetched)
	clientUpdate   bool   // this CLI build is behind latestRelease
	daemonUpdate   bool   // the connected daemon is behind latestRelease
	connectTo      string // set on quit-to-connect; main() acts on this
	connectAgent   string // agent id to attach to alongside connectTo ("" = primary)
	// connectTerminal, when set on quit, attaches to a host terminal instead of
	// an island (a shell on the daemon host).
	connectTerminal string
	// connectShell, when set on quit, opens a contained shell at that island
	// (/workspace inside its container) — what Enter on an island row does.
	connectShell string

	// Host-terminal band (pinned above the island list). bandFocused implies
	// bandExpanded; collapsing clears both (auto-collapse on blur). bandSel is
	// the cursor within the expanded band (terminals, then the "+ new" row).
	bandExpanded bool
	bandFocused  bool
	bandSel      int
	terminals    []hostterm.Terminal // host terminals (empty unless the daemon enables them)
	confirm      *confirmPrompt
	menu         *actionMenu       // non-nil while the per-row action menu is open
	dirtyOps     map[string]string // name → "hibernating" etc. (transient hint)
	building     bool              // island image build in flight

	help         bool            // help overlay visible
	helpAdvanced bool            // advanced section of the help overlay expanded
	creator      *creatorModel   // non-nil while the new-island flow is active
	switcher     *switcherModel  // non-nil while the connection switcher is open
	agentAdder   *agentAdder     // non-nil while the add-agent flow is active
	expanded     map[string]bool // island name → agents-revealed (default: all expanded)

	activeHost   string          // current target: "" = local socket, else host:port
	activeLabel  string          // profile name for the active target, if known
	activeSource string          // where the target came from: "env" | "profile" | "local"
	detailScroll int             // scroll offset (lines) for the detail panel; reset on selection change
	skew         string          // client/daemon version-skew warning, or ""
	editor       string          // preferred Remote-SSH editor CLI ("" = auto-detect); from clientcfg
	settings     *settingsModel  // non-nil while the settings overlay is open
	resEditor    *resourceEditor // non-nil while the per-island resources overlay is open
	modelEditor  *modelEditor    // non-nil while the per-agent model/provider/key overlay is open
	audit        *auditView      // non-nil while the audit-ledger viewer is open (opened with `A`)
	grants       *grantsView     // non-nil while the island-grants trust view is open (opened with `T`)
	scope        *scopeView      // non-nil while the Port scope-picker is open (opened with `P`)
	approvals    *approvalsView  // non-nil while the action-gate approvals overlay is open (opened with `V`)
	// pendingActions is the polled queue of cross-island actions awaiting approval
	// (action gate, Lane 5 P3). Drives the announcement-bar badge; empty when the
	// gate is unused/disabled. See tui_approvals.go.
	pendingActions []link.ActionRequest
	// policyRules are the active auto-approve rules, loaded when the approvals
	// overlay opens (and after a mutation) — not polled. See tui_approvals.go.
	policyRules []policy.Rule
	// updateError is a STICKY client/daemon self-update failure, shown in the
	// header announcement until the next update attempt or an explicit dismiss
	// (esc). Distinct from lastError, which routine 2s polls clear — an update
	// failure that vanished in 2s is exactly the bug #22's reporting was meant to
	// kill, so it gets its own non-transient slot.
	updateError string
	// updateApplied is a GREEN, self-fading success banner (e.g. the daemon
	// updated and is restarting itself — no user action needed). It clears on a
	// fade tick keyed by applyToken so a newer banner is never wiped early.
	updateApplied string
	applyToken    int
	// restartPending is an ORANGE, sticky banner for an update that landed but
	// needs the user to act before it takes effect — the client binary updated,
	// but this running process is still the old one until they relaunch. Stays
	// until restart or an explicit [esc] dismiss.
	restartPending string
}

type confirmPrompt struct {
	verb   string // "purge", "reset", "remove-agent"
	island string
	agent  string // for "remove-agent"
	answer string
	force  bool // for "update-daemon": apply even with terminals attached
}

// actionMenu is the per-row context menu (opened with ⏎ on an island/agent/
// terminal row). It's a discoverability + decluttering layer, NOT a new code
// path: every item carries the single-key accelerator it maps to, and choosing
// it simply re-dispatches that key through handleKey — so the menu and the
// hotkeys can never drift, and power users keep pressing h/w/r/d directly while
// the footer no longer has to advertise all of them.
type actionMenu struct {
	title string
	items []actionMenuItem
	sel   int
	row   treeRow // the row the menu was opened on; re-anchored to on dispatch
}

type actionMenuItem struct {
	label  string // human label, e.g. "Hibernate"
	key    string // the accelerator this dispatches, e.g. "h" (empty when open is set)
	danger bool   // destructive — rendered in alarm color
	// open, when set, is a menu-only action with no global hotkey — chooseMenuItem
	// calls it directly (after re-anchoring) instead of re-dispatching a key.
	open func(tuiModel) (tea.Model, tea.Cmd)
}

func initialTUIModel(c *api.Client) tuiModel {
	host, label, source := resolveTarget()
	cfg, _ := clientcfg.Load()
	return tuiModel{
		client:       c,
		dirtyOps:     map[string]string{},
		expanded:     map[string]bool{},
		activeHost:   host,
		activeLabel:  label,
		activeSource: source,
		editor:       cfg.Editor,
	}
}

// settingsModel is the general-settings overlay (opened with 's'). It's a small
// two-level menu: a top page of preferences (editor, group-by-repo, connection
// target) and an editor sub-page. Global, infrequent controls live here instead
// of each owning a footer hotkey.
type settingsModel struct {
	page settingsPage
	sel  int
}

type settingsPage int

const (
	settingsTop    settingsPage = iota // the preferences list
	settingsEditor                     // the editor radio sub-page
)

type editorChoice struct {
	label string // shown to the user
	cmd   string // CLI command stored in clientcfg.Editor ("" = auto-detect)
}

var editorChoices = []editorChoice{
	{"Auto-detect (first found)", ""},
	{"VS Code", "code"},
	{"Cursor", "cursor"},
	{"Windsurf", "windsurf"},
	{"Antigravity", "antigravity"},
	{"VS Code Insiders", "code-insiders"},
}

// settingsTopLen is the number of rows on the top preferences page.
const settingsTopLen = 5 // editor · group-by-repo · connection target · check-for-updates · update

func (m tuiModel) openSettings() tuiModel {
	m.settings = &settingsModel{page: settingsTop}
	return m
}

func editorIndex(cmd string) int {
	for i, c := range editorChoices {
		if c.cmd == cmd {
			return i
		}
	}
	return 0
}

// settingsKey drives the settings overlay.
func (m tuiModel) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	rows := settingsTopLen
	if s.page == settingsEditor {
		rows = len(editorChoices)
	}
	switch msg.String() {
	case "esc", "q", "ctrl+c", "left", "h":
		if s.page == settingsEditor { // back to the top page, don't close
			s.page, s.sel = settingsTop, 0
			return m, nil
		}
		m.settings = nil
		return m, nil
	case "j", "down":
		if s.sel < rows-1 {
			s.sel++
		}
		return m, nil
	case "k", "up":
		if s.sel > 0 {
			s.sel--
		}
		return m, nil
	case "enter", "right", "l", " ":
		if s.page == settingsTop {
			switch s.sel {
			case 0: // Preferred editor → sub-page
				s.page, s.sel = settingsEditor, editorIndex(m.editor)
				return m, nil
			case 1: // Toggle group-by-repo (stays open)
				return m.toggleGrouped(), nil
			case 2: // Connection target → reuse the existing switcher overlay
				m.settings = nil
				return m.openSwitcher()
			case 3: // Check for updates (re-poll GitHub) — stays open; line refreshes
				m.lastNotice = "checking for updates…"
				return m, tea.Batch(fetchLatestReleaseCmd(), m.fetchOverviewCmd())
			case 4: // Update — same flow as 'U' (confirm, then client/daemon apply)
				m.settings = nil
				m.updateError = ""
				switch {
				case m.clientUpdate:
					m.confirm = &confirmPrompt{verb: "update-client"}
				case m.daemonUpdate:
					m.confirm = &confirmPrompt{verb: "update-daemon"}
				default:
					m.lastNotice = "already up to date"
				}
				return m, nil
			}
		}
		// Editor sub-page: choose + persist, then back to the top page.
		choice := editorChoices[s.sel]
		m.editor = choice.cmd
		cfg, _ := clientcfg.Load()
		cfg.Editor = choice.cmd
		if err := clientcfg.Save(cfg); err != nil {
			m.lastError = "couldn't save settings: " + err.Error()
		} else if choice.cmd == "" {
			m.lastNotice = "editor: auto-detect"
		} else {
			m.lastNotice = "editor set to " + choice.label
		}
		s.page, s.sel = settingsTop, 0
		return m, nil
	}
	return m, nil
}

// toggleGrouped flips repo-grouping and re-anchors the cursor on the island it
// was on (rows reorder). Shared by the settings toggle and the `p` accelerator.
func (m tuiModel) toggleGrouped() tuiModel {
	anchor := m.selectedName()
	m.grouped = !m.grouped
	if anchor != "" {
		for i, row := range m.visibleRows() {
			if row.kind == rowIsland && row.island == anchor {
				m.selected = i
				break
			}
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tickMsg time.Time
type listMsg []api.IslandInfo
type terminalsMsg []hostterm.Terminal
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
type terminalCreatedMsg struct {
	id  string
	err error
}
type terminalRemovedMsg struct{ err error }

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// updateNoticeTTL is how long a green "updated" banner shows before it fades.
const updateNoticeTTL = 5 * time.Second

// updateNoticeFadedMsg fires updateNoticeTTL after a green success banner is
// set; token identifies which banner armed it, so a fade can't wipe a newer one.
type updateNoticeFadedMsg struct{ token int }

func fadeUpdateNoticeCmd(token int) tea.Cmd {
	return tea.Tick(updateNoticeTTL, func(time.Time) tea.Msg { return updateNoticeFadedMsg{token} })
}

// showUpdateApplied arms a self-fading green success banner, clearing any
// competing update banners, and returns the fade command to schedule.
func (m *tuiModel) showUpdateApplied(msg string) tea.Cmd {
	m.applyToken++
	m.updateApplied = msg
	m.restartPending = ""
	m.updateError = ""
	return fadeUpdateNoticeCmd(m.applyToken)
}

// releaseCheckInterval is how often the TUI re-polls GitHub for a newer release
// while it's open, so a release that drops mid-session surfaces on its own. Kept
// long — the GitHub API rate-limits unauthenticated callers, and a fresh release
// is not urgent — but short enough to catch one within a working day.
const releaseCheckInterval = 6 * time.Hour

// releaseTickMsg fires on releaseCheckInterval to trigger a background re-poll;
// distinct from the 2s tickMsg so the rate-limited release check stays slow.
type releaseTickMsg time.Time

func releaseTickCmd() tea.Cmd {
	return tea.Tick(releaseCheckInterval, func(t time.Time) tea.Msg { return releaseTickMsg(t) })
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

// fetchTerminalsCmd loads host terminals, but only once the daemon has said it
// offers them (avoids a 403 on every poll when the feature is off).
func (m tuiModel) fetchTerminalsCmd() tea.Cmd {
	if !m.hostTerminalsEnabled() {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		ts, err := m.client.ListTerminals(ctx)
		if err != nil {
			return errMsg{err}
		}
		return terminalsMsg(ts)
	}
}

func (m tuiModel) createTerminalCmd(label string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		t, err := m.client.CreateTerminal(ctx, label)
		if err != nil {
			return terminalCreatedMsg{err: err}
		}
		return terminalCreatedMsg{id: t.ID}
	}
}

func (m tuiModel) removeTerminalCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return terminalRemovedMsg{err: m.client.DeleteTerminal(ctx, id)}
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
	// Title the dashboard's own terminal tab "dejima" (session tabs it spawns are
	// titled "<island>-<agent>"); see openAgentWindow.
	return tea.Batch(tea.SetWindowTitle("dejima"), m.fetchListCmd(), m.fetchOverviewCmd(), m.fetchSetupReadinessCmd(), fetchLatestReleaseCmd(), tickCmd(), releaseTickCmd())
}

// latestReleaseMsg carries the newest published release tag (or "" on any
// error — the update banner simply stays hidden, never blocks the TUI).
type latestReleaseMsg struct{ latest string }

// fetchLatestReleaseCmd queries GitHub for the latest release tag. Run sparingly
// (Init, manual refresh, and the slow releaseCheckInterval re-poll), never on the
// 2s tick — the GitHub API rate-limits unauthenticated callers.
func fetchLatestReleaseCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		v, err := selfupdate.LatestRelease(ctx)
		if err != nil {
			return latestReleaseMsg{}
		}
		return latestReleaseMsg{latest: v}
	}
}

// daemonUpdateAvailable reports whether the connected daemon is behind latest.
func daemonUpdateAvailable(latest string, o *api.OverviewResponse) bool {
	if latest == "" || o == nil || o.DaemonVersion == "" {
		return false
	}
	return selfupdate.Evaluate(o.DaemonVersion, latest, selfupdate.ModeSource).UpdateAvailable
}

// clientUpdatedMsg is the result of an in-TUI client self-update.
type clientUpdatedMsg struct {
	version string
	err     error
}

// applyClientUpdateCmd downloads + verifies + replaces this binary (release
// installs only). Network IO, so it runs as a command off the UI loop.
func applyClientUpdateCmd(ver string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		err := selfupdate.ApplyReleaseSelf(ctx, ver, io.Discard)
		return clientUpdatedMsg{version: ver, err: err}
	}
}

// daemonUpdatedMsg is the result of asking the daemon to update itself.
type daemonUpdatedMsg struct {
	resp *api.AdminUpdateResponse
	err  error
}

// updateDaemonCmd asks the daemon to update + restart itself. The daemon now
// builds/installs the new binary synchronously (so failures come back as real
// errors) and only restarts afterward — so this can take as long as a `make
// install`; the timeout is generous. The Applying flag confirms the install
// succeeded and the restart has begun.
func (m tuiModel) updateDaemonCmd(force bool) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		resp, err := c.DaemonUpdate(ctx, true, force)
		return daemonUpdatedMsg{resp: resp, err: err}
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		cmds := []tea.Cmd{tickCmd(), m.fetchListCmd(), m.fetchOverviewCmd(), m.fetchPendingActionsCmd()}
		if name := m.selectedName(); name != "" {
			cmds = append(cmds, m.fetchDetailCmd(name))
		}
		if c := m.fetchTerminalsCmd(); c != nil {
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)

	case pendingActionsMsg:
		m.pendingActions = msg
		if m.approvals != nil && m.approvals.sel >= len(msg) {
			m.approvals.sel = max(0, len(msg)-1)
		}
		return m, nil

	case policyRulesMsg:
		m.policyRules = msg
		if m.approvals != nil {
			if m.approvals.ruleSel >= len(msg) {
				m.approvals.ruleSel = max(0, len(msg)-1)
			}
			if len(msg) == 0 && m.approvals.focus == focusRules {
				m.approvals.focus = focusPending // nothing left to act on
			}
		}
		return m, nil

	case releaseTickMsg:
		// Re-poll GitHub and re-arm the slow ticker. The result (latestReleaseMsg)
		// recomputes clientUpdate/daemonUpdate, so a release that dropped mid-
		// session lights up the announcement bar without an R or a relaunch.
		return m, tea.Batch(releaseTickCmd(), fetchLatestReleaseCmd())

	case terminalsMsg:
		m.terminals = msg
		m.lastError = ""
		if n := m.rowCount(); m.selected >= n && n > 0 {
			m.selected = n - 1
		}
		// Keep the band cursor in range when a terminal is added/removed.
		if m.bandSel >= m.bandRowCount() {
			m.bandSel = m.bandRowCount() - 1
		}
		return m, nil

	case terminalCreatedMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
			return m, nil
		}
		m.connectTerminal = msg.id // attach to the freshly created terminal
		return m, tea.Quit

	case terminalRemovedMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
		return m, m.fetchTerminalsCmd()

	case listMsg:
		m.islands = sortIslands(msg)
		m.lastError = ""
		m.daemonHelp = nil // a successful load means the daemon is back
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
			// Resolve the SSH endpoint once per distinct addr (endpointFromAddr
			// may exec `tailscale`), not every render, so the detail panel can
			// show a connect string cheaply.
			if msg.SSHAddr != "" && msg.SSHAddr != m.sshResolvedFor {
				if h, p, err := endpointFromAddr(msg.SSHAddr, m.client.DaemonHost()); err == nil {
					m.sshHost, m.sshPort, m.sshResolvedFor = h, p, msg.SSHAddr
				}
			}
			m.daemonUpdate = daemonUpdateAvailable(m.latestRelease, msg)
		}
		return m, m.fetchTerminalsCmd() // nil (no-op) unless host terminals are on

	case setupReadinessMsg:
		m.setupChecked = true
		m.claudeSeeded = msg.claudeSeeded
		m.agentKeyGap = msg.keyGap
		return m, nil

	case latestReleaseMsg:
		if msg.latest != "" {
			m.latestRelease = msg.latest
			m.clientUpdate = selfupdate.Evaluate(version.Version, msg.latest, selfupdate.DetectMode()).UpdateAvailable
			m.daemonUpdate = daemonUpdateAvailable(msg.latest, m.overview)
		}
		return m, nil

	case clientUpdatedMsg:
		if msg.err != nil {
			m.updateError = "client update failed: " + msg.err.Error()
			m.updateApplied, m.restartPending = "", ""
		} else {
			// The binary on disk is new, but THIS process is still the old one —
			// it only takes effect on relaunch. Orange + sticky until they do.
			m.clientUpdate = false
			m.updateError, m.updateApplied = "", ""
			m.restartPending = "client updated to " + msg.version + " — restart dejima to apply"
		}
		return m, nil

	case daemonUpdatedMsg:
		switch {
		case msg.err != nil:
			// The install now runs synchronously, so an error here is a real
			// failure (git pull / make install / missing sudoers) — surface it
			// stickily (updateError), since the transient lastError would be
			// wiped by the next 2s poll before it could be read.
			m.updateError = "daemon update failed: " + msg.err.Error()
			m.updateApplied, m.restartPending = "", ""
		case msg.resp != nil && msg.resp.Applying:
			// The daemon restarts itself and reconnects — no user action needed,
			// so this is a green "done" that fades on its own.
			m.daemonUpdate = false
			cmd := m.showUpdateApplied("daemon updated to " + msg.resp.Latest + " — restarting, reconnecting shortly")
			return m, cmd
		case msg.resp != nil && msg.resp.Deferred:
			// The restart would drop every attached terminal, so the daemon held
			// off. Re-prompt to force (the y/n confirm sets force=true), or let the
			// operator detach those terminals first and retry.
			n := msg.resp.AttachedClients
			m.confirm = &confirmPrompt{verb: "update-daemon", force: true}
			m.lastNotice = fmt.Sprintf("update deferred: %s, closing every open terminal — force anyway?", pluralizeTerminals(n))
		default:
			m.lastNotice = "daemon already up to date"
		}
		return m, nil

	case updateNoticeFadedMsg:
		// Only clear if this is still the banner that armed the fade — a newer
		// success set since then owns the slot and its own later fade.
		if msg.token == m.applyToken {
			m.updateApplied = ""
		}
		return m, nil

	case auditLoadedMsg:
		if m.audit != nil {
			m.audit.applyLoaded(msg)
		}
		return m, nil

	case grantsLoadedMsg:
		if m.grants != nil {
			m.grants.applyLoaded(msg)
		}
		return m, nil

	case scopesLoadedMsg:
		if m.scope != nil && m.scope.island == msg.island {
			m.scope.applyLoaded(msg)
		}
		return m, nil

	case scopeMutatedMsg:
		if m.scope != nil && m.scope.island == msg.island {
			m.scope.busy = false
			if msg.err != nil {
				m.scope.errText = msg.verb + ": " + msg.err.Error()
				return m, nil
			}
			// Reload so the list reflects the grant/revoke immediately.
			m.scope.loading = true
			return m, m.loadScopesCmd(msg.island)
		}
		return m, nil

	case resourcesUpdatedMsg:
		if m.resEditor != nil {
			m.resEditor.busy = false
		}
		if msg.err != nil {
			m.lastError = "resources: " + msg.err.Error()
		} else {
			m.resEditor = nil
			if msg.restartRequired {
				// OOM priority is create-time, so it only takes effect on a
				// container recreate — offer to do that now, for this island only.
				m.confirm = &confirmPrompt{verb: "recreate-island", island: msg.island}
			} else {
				m.lastNotice = "resources updated"
			}
		}
		return m, tea.Batch(m.fetchListCmd(), m.fetchDetailCmd(msg.island))

	case modelEditorLoadedMsg:
		if m.modelEditor != nil && m.modelEditor.agentID == msg.agentID {
			if msg.err != nil {
				m.lastError = "load agent capabilities: " + msg.err.Error()
				m.modelEditor = nil
			} else {
				m.modelEditor.applyLoaded(msg)
			}
		}
		return m, nil

	case providerKeySetMsg:
		if m.modelEditor != nil {
			m.modelEditor.busy = false
			if msg.err != nil {
				m.lastError = "set provider key: " + msg.err.Error()
			} else {
				m.modelEditor.keySet = true
				m.modelEditor.enteringKey = false
				m.modelEditor.keyInput = ""
				m.lastNotice = "key set for " + msg.provider + " — applies to all " + msg.provider + " agents"
			}
		}
		return m, nil

	case agentConfiguredMsg:
		if m.modelEditor != nil {
			m.modelEditor.busy = false
		}
		if msg.err != nil {
			m.lastError = "agent config: " + msg.err.Error()
		} else {
			island := ""
			if m.modelEditor != nil {
				island = m.modelEditor.island
			}
			m.modelEditor = nil
			if msg.restartRequired {
				m.confirm = &confirmPrompt{verb: "recreate-island", island: island}
			} else {
				m.lastNotice = "agent model updated"
			}
			return m, tea.Batch(m.fetchListCmd(), m.fetchDetailCmd(island))
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
			// A local daemon that's gone unreachable: attach a one-shot, actionable
			// diagnosis (computed here, not in the renderer, since it shells out).
			if m.activeHost == "" && isConnectionError(msg.err) {
				d := diagnoseLocalDaemon()
				m.daemonHelp = &d
			}
		}
		return m, nil

	case opCompleteMsg:
		delete(m.dirtyOps, msg.name)
		if msg.err != nil {
			// A purge blocked by the unpushed-work guard ends with "...--force...".
			// Offer a force-purge confirmation instead of just surfacing the error,
			// so the operator can override from the TUI without dropping to the CLI.
			if msg.verb == "purge" && strings.Contains(msg.err.Error(), "--force") {
				m.lastError = msg.err.Error()
				m.confirm = &confirmPrompt{verb: "force-purge", island: msg.name}
				return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
			}
			m.lastError = fmt.Sprintf("%s %s: %v", msg.verb, msg.name, msg.err)
		}
		cmds := []tea.Cmd{m.fetchListCmd(), m.fetchOverviewCmd(), m.fetchPendingActionsCmd()}
		if m.approvals != nil {
			// Refresh the rules section too while the overlay's open (covers
			// approve+rule and revoke); skipped otherwise to avoid a needless GET.
			cmds = append(cmds, m.fetchPolicyCmd())
		}
		return m, tea.Batch(cmds...)

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

	case ghIdentitiesMsg:
		if m.creator != nil {
			return m.onGhIdentities(msg)
		}
		return m, nil

	case ghReposMsg:
		if m.creator != nil {
			m.creator.onGhRepos(msg)
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
		// Launch the freshly-added agent in a new tab, leaving the dashboard up.
		// agentLabel is passed through because the list refresh below is async, so
		// m.islands doesn't yet carry the new agent for the title lookup.
		if canOpenNewWindow() {
			var err error
			if msg.attachable {
				err = m.openInNewWindow(msg.island, msg.agentID, msg.agentLabel)
			} else {
				err = m.openAgentLogsWindow(msg.island, msg.agentID, msg.agentLabel)
			}
			if err != nil {
				m.lastError = err.Error()
			}
		}
		return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())

	case islandCreatedMsg:
		if m.creator != nil {
			if msg.err != nil {
				m.creator.creating = false
				m.creator.err = msg.err.Error()
				return m, nil
			}
			m.creator = nil
			// Open the new island in a new tab so the dashboard stays up; fall back
			// to attaching in this terminal when there's no new-window backend.
			if canOpenNewWindow() {
				if err := m.openInNewWindow(msg.name, "", ""); err != nil {
					m.lastError = err.Error()
				} else {
					m.lastNotice = "created " + msg.name + " — opened in a new tab"
				}
				return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
			}
			m.connectTo = msg.name // drop straight into the new island's session
			return m, tea.Quit
		}
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A success notice lingers until the next keystroke, then clears (an action
	// may set a fresh one — e.g. setup-ssh sets it via runConfirmed below).
	m.lastNotice = ""
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
	// The per-row action menu owns keys while open.
	if m.menu != nil {
		return m.actionMenuKey(msg)
	}
	// The settings overlay owns keys while open.
	if m.settings != nil {
		return m.settingsKey(msg)
	}
	// The per-island resources overlay owns keys while open.
	if m.resEditor != nil {
		return m.resEditorKey(msg)
	}
	// The per-agent model/provider/key overlay owns keys while open.
	if m.modelEditor != nil {
		return m.modelEditorKey(msg)
	}
	// The audit-ledger viewer owns keys while open.
	if m.audit != nil {
		return m.auditKey(msg)
	}
	// The island-grants trust view owns keys while open.
	if m.grants != nil {
		return m.grantsKey(msg)
	}
	// The Port scope-picker owns keys while open.
	if m.scope != nil {
		return m.scopeKey(msg)
	}
	// The action-gate approvals overlay owns keys while open.
	if m.approvals != nil {
		return m.approvalsKey(msg)
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
	// The host-terminal band owns keys while focused (expanded + driving). After
	// the confirm guard, so a band-opened "close terminal" confirm takes keys.
	if m.bandFocused {
		return m.bandKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "A":
		// Audit-ledger viewer (chain-verification + recent governance activity).
		return m.openAuditView()
	case "T":
		// Trust surface — what the highlighted island can reach (Port · MCP ·
		// links · capabilities). Agent rows inherit their island's grants.
		return m.openGrantsView(m.selectedName())
	case "V":
		// Action-gate approvals — the queue of cross-island actions awaiting a
		// decision (reView) + the active auto-approve rules. Refresh both on open.
		// (G is jump-to-bottom.)
		m.approvals = &approvalsView{}
		return m, tea.Batch(m.fetchPendingActionsCmd(), m.fetchPolicyCmd())
	case "P":
		// Port scope-picker for the selected island (brokered host-file grants).
		// Capital P; lowercase p is group-by-repo.
		if name := m.selectedName(); name != "" {
			return m.openScopeView(name)
		}
		return m, nil
	case "n":
		return m.openCreator()
	case "`":
		// Toggle + focus the pinned host-terminal band (above the island list).
		if m.hostTerminalsEnabled() {
			m.bandExpanded = true
			m.bandFocused = true
			if m.bandSel >= m.bandRowCount() {
				m.bandSel = 0
			}
		}
		return m, nil
	case "t":
		// New host terminal (uncontained shell on the daemon host) + attach.
		if m.hostTerminalsEnabled() {
			return m, m.createTerminalCmd("")
		}
	case "s", ",":
		// General settings (editor · group-by-repo · connection target). Server
		// switching now lives inside here rather than owning its own hotkey.
		return m.openSettings(), nil
	case "$":
		// In-island /workspace shell for the selected island. (Enter now opens the
		// island's agents; `$` — the shell prompt — opens the contained shell.)
		name := m.selectedName()
		if name == "" {
			return m, nil
		}
		if m.detail != nil && m.detail.Container != "running" {
			m.lastError = fmt.Sprintf("island %q is %s; `w` to wake it first", name, m.detail.Container)
			return m, nil
		}
		return m.openIslandShell(name)
	case "j", "down":
		if m.selected < m.rowCount()-1 {
			m.selected++
			m.detailScroll = 0
			return m, m.fetchDetailCmd(m.selectedName())
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
			m.detailScroll = 0
			return m, m.fetchDetailCmd(m.selectedName())
		}
	case "g", "home":
		m.selected = 0
		m.detailScroll = 0
		return m, m.fetchDetailCmd(m.selectedName())
	case "G", "end":
		m.selected = m.rowCount() - 1
		m.detailScroll = 0
		return m, m.fetchDetailCmd(m.selectedName())
	case "pgdown", "ctrl+d":
		return m.scrollDetail(1), nil
	case "pgup", "ctrl+u":
		return m.scrollDetail(-1), nil
	case "enter", "o":
		// ⏎ opens the highlighted row — the primary, frequent action. For an
		// island/agent that means its session in a new TAB (tmux window / Windows
		// Terminal tab / a new terminal), keeping this dashboard up; for an
		// affordance row it runs the creator / add-agent flow. The per-row action
		// menu lives on `m`, not here, so opening never costs an extra keystroke.
		return m.activateRow()
	case "m":
		// Per-row action menu — the lifecycle/setup actions (hibernate, reset,
		// rename, ssh setup, purge…) that used to crowd the footer, now hanging
		// off the highlighted island/agent/terminal row.
		if mm, ok := m.openActionMenu(); ok {
			return mm, nil
		}
		return m, nil
	case "c":
		// Open the island's repo in VS Code / Cursor over Remote-SSH, straight at
		// /workspace — no folder-browsing. Needs the SSH façade (so the dejima-<island>
		// host exists) and a local editor CLI.
		if name := m.selectedName(); name != "" {
			if m.overview == nil || m.overview.SSHAddr == "" {
				m.lastError = "ssh façade is off — press m → SSH setup, or start dejimad with --ssh"
				return m, nil
			}
			if err := openInEditor("dejima-"+name, m.editor); err != nil {
				m.lastError = err.Error()
			} else {
				m.lastNotice = "opening " + name + " in your editor at /workspace…"
			}
		}
		return m, nil
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
	case "p":
		// Toggle grouping the island list by repo (sibling/project view). Also
		// reachable from Settings; kept as a power-user accelerator.
		return m.toggleGrouped(), nil
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
		// Remove the selected agent (agent rows only). An island may have zero
		// agents — you can still shell into it. The daemon enforces the one
		// exception (a headless first agent that is the container's PID 1) and
		// surfaces it as an error here.
		if r := m.currentRow(); r.kind == rowAgent {
			m.confirm = &confirmPrompt{verb: "remove-agent", island: r.island, answer: "", agent: r.agentID}
		}
	case "[", "]":
		// Reorder the highlighted agent within its island (cosmetic). `[` moves it
		// up/earlier, `]` down/later; the cursor follows the agent.
		r := m.currentRow()
		if r.kind != rowAgent {
			return m, nil
		}
		isl, ok := m.islandByName(r.island)
		if !ok {
			return m, nil
		}
		idx := -1
		for i, a := range isl.Agents {
			if a.ID == r.agentID {
				idx = i
				break
			}
		}
		delta := -1
		if msg.String() == "]" {
			delta = 1
		}
		// No-op at the ends.
		if idx < 0 || (delta < 0 && idx == 0) || (delta > 0 && idx == len(isl.Agents)-1) {
			return m, nil
		}
		m.selected += delta // agent rows are contiguous + in order, so the cursor follows
		return m, m.moveAgentCmd(r.island, r.agentID, delta)
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
	case "v":
		// Configure the agent's LLM provider/model + key (key-requiring types).
		if r := m.currentRow(); r.kind == rowIsland || r.kind == rowAgent {
			return m.openModelEditor(r.island, r.agentID)
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
	case "S":
		// Set up SSH for the whole account: authorize this machine's key
		// fleet-wide + write ~/.ssh/config entries for every island (the TUI
		// equivalent of `ssh authorize --account` + `ssh config --all --install`).
		if m.overview == nil || m.overview.SSHAddr == "" {
			m.lastError = "ssh façade is off — start dejimad with --ssh (e.g. `dejima service install --ssh :2222`)"
			return m, nil
		}
		m.confirm = &confirmPrompt{verb: "setup-ssh"}
		return m, nil
	case "esc":
		// Dismiss whichever sticky update banner is showing (no overlay here):
		// a failure, or an applied-but-needs-restart notice. (Green fades itself.)
		m.updateError = ""
		m.restartPending = ""
		return m, nil
	case "U":
		// Update Dejima itself (distinct from lowercase 'u' = upgrade an island).
		m.updateError = "" // clear a prior failure when retrying
		switch {
		case m.clientUpdate:
			m.confirm = &confirmPrompt{verb: "update-client"}
		case m.daemonUpdate:
			m.confirm = &confirmPrompt{verb: "update-daemon"}
		default:
			m.lastNotice = "already up to date"
		}
		return m, nil
	case "R":
		return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd(), fetchLatestReleaseCmd())
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
	case "recreate-island":
		// Recreate the container so a create-time setting (OOM priority) applies.
		// Same mechanism as upgrade (recreate on the current image; workspace and
		// agent state preserved) — just a different prompt.
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			m.dirtyOps[c.island] = "restarting"
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
	case "force-purge":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			m.lastError = ""
			m.dirtyOps[c.island] = "purging"
			return m, m.opCmd(c.island, "purge-force")
		}
	case "remove-agent":
		// Require typing the agent id (parity with island purge typing the name) —
		// a destructive op shouldn't go through on a single keystroke.
		if strings.TrimSpace(c.answer) == c.agent {
			m.dirtyOps[c.island] = "removing agent"
			return m, m.removeAgentCmd(c.island, c.agent)
		}
	case "remove-terminal":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			return m, m.removeTerminalCmd(c.agent) // c.agent carries the terminal id
		}
	case "approve-action":
		// Approving a DESTRUCTIVE cross-island action: require a typed "y" so it
		// can't be rubber-stamped. (c.agent carries the action id.)
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			return m, m.approveActionCmd(c.agent)
		}
	case "deny-action":
		// The typed text is an optional ledger reason (blank is fine); deny always
		// proceeds. (c.agent carries the action id.)
		return m, m.denyActionCmd(c.agent, strings.TrimSpace(c.answer))
	case "approve-rule":
		// Approve + add a scoped auto-approve rule. The typed answer is
		// "<max> [<ttl>]" (e.g. "20 1h"); blank max = unlimited, blank ttl = no
		// expiry. Look the action up by id so a queue refresh can't misdirect it.
		if a, ok := m.findPendingAction(c.agent); ok {
			maxCount, ttl := parseRuleSpec(c.answer)
			return m, m.approveRuleCmd(a, maxCount, ttl)
		}
	case "open-all-agents":
		// Confirmed opening many agent windows at once (Enter on a big island).
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			return m.openAgents(c.island, m.attachableAgentIDs(c.island))
		}
	case "relabel-agent":
		// The typed text is the new label (blank clears it); no y/n gate.
		m.dirtyOps[c.island] = "renaming agent"
		return m, m.relabelAgentCmd(c.island, c.agent, strings.TrimSpace(c.answer))
	case "rename-island":
		// The typed text is the new display title (blank resets to the name).
		m.dirtyOps[c.island] = "renaming"
		return m, m.setIslandTitleCmd(c.island, strings.TrimSpace(c.answer))
	case "setup-ssh":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			return m.setupAccountSSH()
		}
	case "update-client":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			return m, applyClientUpdateCmd(m.latestRelease)
		}
	case "update-daemon":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			return m, m.updateDaemonCmd(c.force)
		}
	}
	return m, nil
}

// setupAccountSSH authorizes this machine's default SSH key fleet-wide and
// writes a ~/.ssh/config entry for every island, so VS Code / Cursor can connect
// to any island with no further setup. Local, fast operations; on success a
// notice shows the result, on failure lastError explains. (No stdout — the
// shared CLI helpers' print-free cores are used.)
func (m tuiModel) setupAccountSSH() (tea.Model, tea.Cmd) {
	pub, err := defaultPublicKey()
	if err != nil {
		m.lastError = err.Error()
		return m, nil
	}
	line, err := os.ReadFile(pub)
	if err != nil {
		m.lastError = "read " + pub + ": " + err.Error()
		return m, nil
	}
	// Enroll via the daemon API (not a local file write): the daemon owns the
	// authorized_keys file, so this works whether the daemon is local or remote
	// and avoids the user-vs-root ~/.dejima ownership snag on a system service.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := m.client.AuthorizeAccountKey(ctx, strings.TrimSpace(string(line))); err != nil {
		m.lastError = "enroll device key: " + err.Error()
		return m, nil
	}
	host, port, err := endpointFromAddr(m.overview.SSHAddr, m.client.DaemonHost())
	if err != nil {
		m.lastError = err.Error()
		return m, nil
	}
	for _, isl := range m.islands {
		if _, _, werr := writeSSHConfigEntry(isl.Name, sshConfigBlock(isl.Name, host, port)); werr != nil {
			m.lastError = "write ssh config: " + werr.Error()
			return m, nil
		}
	}
	m.lastError = ""
	m.lastNotice = fmt.Sprintf("ssh ready — key authorized for all islands; %d ~/.ssh/config entr%s written. VS Code/Cursor: Remote-SSH → dejima-<island>",
		len(m.islands), pluralY(len(m.islands)))
	return m, nil
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// pluralizeTerminals renders an attached-terminal count for the deferred-update
// notice, e.g. "1 terminal attached" / "3 terminals attached".
func pluralizeTerminals(n int) string {
	if n == 1 {
		return "1 terminal attached"
	}
	return fmt.Sprintf("%d terminals attached", n)
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

// moveAgentCmd reorders an agent within its island by delta positions.
func (m tuiModel) moveAgentCmd(name, agentID string, delta int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := m.client.MoveAgent(ctx, name, agentID, delta)
		return opCompleteMsg{name: name, verb: "reorder agent", err: err}
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
			err = m.client.DeleteIsland(ctx, name, false)
		case "purge-force":
			err = m.client.DeleteIsland(ctx, name, true)
		}
		return opCompleteMsg{name: name, verb: verb, err: err}
	}
}

// rowKind tags each visible line in the island→agent tree.
type rowKind int

const (
	rowIsland      rowKind = iota // an island header (also the primary, when collapsed)
	rowAgent                      // an agent under an expanded island
	rowAddAgent                   // the "+ add agent" affordance under an expanded island
	rowNewIsland                  // the trailing "+ new island" affordance
	rowTerminal                   // a host terminal (uncontained shell) in the Host section
	rowNewTerminal                // the "+ new terminal" affordance
)

// treeRow is one visible line in the list.
type treeRow struct {
	kind    rowKind
	island  string
	agentID string
}

// hostTerminalsEnabled reports whether the daemon offers host terminals (so the
// TUI shows the Host section). Driven by the overview capability.
func (m tuiModel) hostTerminalsEnabled() bool {
	return m.overview != nil && m.overview.HostTerminalsEnabled
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
	for _, isl := range m.orderedIslands() {
		rows = append(rows, treeRow{kind: rowIsland, island: isl.Name})
		if m.islandExpanded(isl) {
			for _, a := range isl.Agents {
				rows = append(rows, treeRow{kind: rowAgent, island: isl.Name, agentID: a.ID})
			}
			rows = append(rows, treeRow{kind: rowAddAgent, island: isl.Name})
		}
	}
	rows = append(rows, treeRow{kind: rowNewIsland})
	// Host terminals (operator shells on the daemon host) used to live at the
	// tail of this list; they now have their own pinned band above it. See
	// renderBand / bandKey.
	return rows
}

// orderedIslands returns the islands in display order: m.islands as-is, or —
// when grouped — reordered so islands sharing a repo are contiguous (first-seen
// repo order, original order within each repo). Drives both the row list and the
// rendered group headers, so navigation indices and headers stay consistent.
func (m tuiModel) orderedIslands() []api.IslandInfo {
	if !m.grouped {
		return m.islands
	}
	idx := map[string]int{}
	var groups [][]api.IslandInfo
	for _, isl := range m.islands {
		i, ok := idx[isl.Repo]
		if !ok {
			i = len(groups)
			idx[isl.Repo] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], isl)
	}
	out := make([]api.IslandInfo, 0, len(m.islands))
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
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
// openActionMenu builds the context menu for the highlighted row, gated on the
// row kind and (for islands) running/hibernated state so it only ever offers
// actions that make sense right now. Returns ok=false for rows that have no
// menu (affordances), so ⏎ falls through to activateRow.
func (m tuiModel) openActionMenu() (tuiModel, bool) {
	row := m.currentRow()
	var (
		title string
		items []actionMenuItem
	)
	switch row.kind {
	case rowIsland:
		isl, ok := m.islandByName(row.island)
		if !ok {
			return m, false
		}
		title = "island · " + isl.Name + "  (" + isl.Container + ")"
		if isl.Container == "running" {
			items = append(items,
				actionMenuItem{label: "Open (new tab)", key: "o"},
				actionMenuItem{label: "Attach in this terminal", key: "a"},
				actionMenuItem{label: "Add an agent", key: "+"},
				actionMenuItem{label: "Hibernate", key: "h"},
			)
			if m.overview != nil && m.overview.SSHAddr != "" {
				items = append(items, actionMenuItem{label: "Open in VS Code / Cursor (/workspace)", key: "c"})
			}
		} else {
			items = append(items, actionMenuItem{label: "Wake", key: "w"})
		}
		items = append(items, actionMenuItem{label: "Rename", key: "e"})
		islandName := isl.Name
		items = append(items, actionMenuItem{label: "Resources… (memory · OOM priority)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openResourceEditor(islandName)
		}})
		items = append(items, actionMenuItem{label: "Grants… (what it can reach)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openGrantsView(islandName)
		}})
		items = append(items, actionMenuItem{label: "Port scopes… (brokered host-file access)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openScopeView(islandName)
		}})
		if m.overview != nil && m.overview.SSHAddr != "" {
			items = append(items, actionMenuItem{label: "SSH setup (this device → every island)", key: "S"})
		}
		if isl.Container == "running" {
			items = append(items,
				actionMenuItem{label: "Reset agent state", key: "r", danger: true},
				actionMenuItem{label: "Upgrade to the current image", key: "u"},
			)
		}
		items = append(items, actionMenuItem{label: "Purge island", key: "d", danger: true})
	case rowAgent:
		isl, _ := m.islandByName(row.island)
		label := agentByID(isl, row.agentID).Label
		if label == "" {
			label = row.agentID
		}
		title = "agent · " + label
		if m.isHeadlessAgent(row.island, row.agentID) {
			items = append(items, actionMenuItem{label: "View logs", key: "o"})
		} else {
			items = append(items,
				actionMenuItem{label: "Open (new tab)", key: "o"},
				actionMenuItem{label: "Attach in this terminal", key: "a"},
			)
		}
		agentIsland, agentRowID := row.island, row.agentID
		items = append(items,
			actionMenuItem{label: "Model / provider / key…", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
				return mm.openModelEditor(agentIsland, agentRowID)
			}},
			actionMenuItem{label: "Grants… (what its island can reach)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
				return mm.openGrantsView(agentIsland)
			}},
			actionMenuItem{label: "Rename (relabel)", key: "e"},
			actionMenuItem{label: "Remove agent", key: "X", danger: true},
		)
	default:
		return m, false
	}
	m.menu = &actionMenu{title: title, items: items, row: row}
	return m, true
}

// actionMenuKey drives the open menu: navigate, select (re-dispatching the
// chosen item's accelerator through handleKey), or close. Pressing an item's
// own accelerator key selects it directly.
func (m tuiModel) actionMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.menu = nil
		return m, nil
	case "j", "down":
		if m.menu.sel < len(m.menu.items)-1 {
			m.menu.sel++
		}
		return m, nil
	case "k", "up":
		if m.menu.sel > 0 {
			m.menu.sel--
		}
		return m, nil
	case "enter", "right", "l":
		return m.chooseMenuItem(m.menu.items[m.menu.sel])
	}
	// A direct accelerator press jumps straight to that item.
	for _, it := range m.menu.items {
		if it.key != "" && it.key == msg.String() {
			return m.chooseMenuItem(it)
		}
	}
	return m, nil
}

// chooseMenuItem closes the menu and replays the item's accelerator through the
// normal key path — the single dispatch point, so menu and hotkeys never drift.
// It first re-anchors the cursor to the row the menu was opened on: a background
// list refresh can reorder rows (islands sort running-first, so a state flip
// shifts selection), and the accelerator handlers act on currentRow — without
// this, a destructive action like purge could land on the wrong island.
func (m tuiModel) chooseMenuItem(it actionMenuItem) (tea.Model, tea.Cmd) {
	target := m.menu.row
	m.menu = nil
	idx := -1
	for i, r := range m.visibleRows() {
		if r == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		// The row vanished while the menu was open (purged/closed elsewhere).
		m.lastError = "selection changed — reopen the menu"
		return m, nil
	}
	m.selected = idx
	if it.open != nil {
		return it.open(m)
	}
	return m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(it.key)})
}

// resourceEditor is the per-island Resources overlay (memory limit + OOM
// priority). Memory applies live; an OOM-priority change takes effect on the
// island's next restart (the daemon flags restart_required).
type resourceEditor struct {
	island  string
	field   int // 0 = memory, 1 = priority
	memSel  int
	prioSel int
	busy    bool
}

var memPresets = []struct{ label, value string }{
	{"unlimited", ""}, {"2G", "2G"}, {"4G", "4G"}, {"8G", "8G"}, {"16G", "16G"},
}

// prioPresets are ordered most→least protected; values match the api presets
// (critical +100 / normal 0 / expendable −100).
var prioPresets = []struct {
	label string
	value int
}{
	{"critical — killed last", 100},
	{"normal", 0},
	{"expendable — killed first (self-restarting brains)", -100},
}

// prioPresetIndex maps a stored priority to the nearest preset row (exact values
// land exactly; a custom stack-rank value buckets by sign).
func prioPresetIndex(v int) int {
	switch {
	case v > 0:
		return 0
	case v < 0:
		return 2
	default:
		return 1
	}
}

// oomTierLabel names a stored priority for display (nil = unset/default).
func oomTierLabel(v *int) string {
	if v == nil {
		return "normal"
	}
	switch {
	case *v > 0:
		return fmt.Sprintf("critical (%d)", *v)
	case *v < 0:
		return fmt.Sprintf("expendable (%d)", *v)
	default:
		return "normal"
	}
}

func (m tuiModel) openResourceEditor(island string) (tea.Model, tea.Cmd) {
	ed := &resourceEditor{island: island, memSel: 0, prioSel: 1} // unlimited · normal
	if m.detail != nil && m.detail.Name == island && m.detail.Resources != nil {
		r := m.detail.Resources
		for i, p := range memPresets {
			if p.value == r.Memory {
				ed.memSel = i
				break
			}
		}
		if r.OOMPriority != nil {
			ed.prioSel = prioPresetIndex(*r.OOMPriority)
		}
	}
	m.resEditor = ed
	return m, nil
}

func (m tuiModel) resEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := m.resEditor
	if ed.busy {
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.resEditor = nil
		return m, nil
	case "down", "j":
		if ed.field < 1 {
			ed.field++
		}
		return m, nil
	case "up", "k":
		if ed.field > 0 {
			ed.field--
		}
		return m, nil
	case "left", "h":
		if ed.field == 0 {
			ed.memSel = (ed.memSel - 1 + len(memPresets)) % len(memPresets)
		} else {
			ed.prioSel = (ed.prioSel - 1 + len(prioPresets)) % len(prioPresets)
		}
		return m, nil
	case "right", "l", " ":
		if ed.field == 0 {
			ed.memSel = (ed.memSel + 1) % len(memPresets)
		} else {
			ed.prioSel = (ed.prioSel + 1) % len(prioPresets)
		}
		return m, nil
	case "enter":
		ed.busy = true
		return m, m.applyResourcesCmd(ed.island, memPresets[ed.memSel].value, prioPresets[ed.prioSel].value)
	}
	return m, nil
}

type resourcesUpdatedMsg struct {
	island          string
	err             error
	restartRequired bool
}

func (m tuiModel) applyResourcesCmd(island, mem string, prio int) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := c.UpdateIslandResources(ctx, island, api.UpdateResourcesRequest{Memory: &mem, OOMPriority: &prio})
		return resourcesUpdatedMsg{island: island, err: err, restartRequired: resp != nil && resp.RestartRequired}
	}
}

func (m tuiModel) renderResourceEditor() string {
	ed := m.resEditor
	var b strings.Builder
	b.WriteString(styleHeader.Render("Resources — " + ed.island))
	b.WriteString("\n\n")
	rows := [2]struct{ label, val string }{
		{"Memory limit", memPresets[ed.memSel].label},
		{"OOM priority", prioPresets[ed.prioSel].label},
	}
	for i, r := range rows {
		lead := "   "
		val := styleMuted.Render("‹ ") + r.val + styleMuted.Render(" ›")
		if i == ed.field {
			lead = styleAccent.Render(" ▸ ")
			val = styleSelected.Render(" ‹ " + r.val + " › ")
		}
		b.WriteString(fmt.Sprintf("%s%-14s %s\n", lead, r.label, val))
	}
	b.WriteString("\n")
	if ed.busy {
		b.WriteString(styleAccent.Render("applying…"))
		return b.String()
	}
	b.WriteString(styleMuted.Render("↑/↓ field · ←/→ change · ⏎ apply · esc cancel"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("memory applies live; OOM priority on next restart"))
	return b.String()
}

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
	// Enter on an island row opens all of its (attachable) agents, each in its
	// own window — "jump into this island's work". The in-island shell moved to
	// `$`. (Agents are also openable one at a time via Enter on their own row.)
	if row.agentID == "" {
		return m.openIslandAgents(name)
	}
	if m.isHeadlessAgent(name, row.agentID) {
		return m.openAgentLogs(name, row.agentID)
	}
	if canOpenNewWindow() {
		if err := m.openInNewWindow(name, row.agentID, ""); err != nil {
			m.lastError = err.Error()
		}
		return m, nil
	}
	m.connectTo, m.connectAgent = name, row.agentID
	return m, tea.Quit
}

// openAllConfirmThreshold is the number of windows above which Enter-on-island
// asks before spawning them all (so a stray Enter on a big island can't blanket
// the screen in tabs).
const openAllConfirmThreshold = 4

// openIslandAgents opens one window per attachable agent on the island (Enter on
// an island row). It falls back to the in-island shell when there's nothing to
// open as windows — an empty or all-headless island, or a terminal that can't
// spawn windows — and confirms first past openAllConfirmThreshold.
func (m tuiModel) openIslandAgents(name string) (tea.Model, tea.Cmd) {
	ids := m.attachableAgentIDs(name)
	if !canOpenNewWindow() || len(ids) == 0 {
		return m.openIslandShell(name)
	}
	if len(ids) > openAllConfirmThreshold {
		m.confirm = &confirmPrompt{verb: "open-all-agents", island: name}
		return m, nil
	}
	return m.openAgents(name, ids)
}

// openAgents opens a window for each given agent id; errors surface but don't
// stop the rest from opening.
func (m tuiModel) openAgents(name string, ids []string) (tea.Model, tea.Cmd) {
	for _, id := range ids {
		if err := m.openInNewWindow(name, id, ""); err != nil {
			m.lastError = err.Error()
		}
	}
	return m, nil
}

// openIslandShell attaches the local terminal to the island's in-island shell at
// /workspace — in a new window when the terminal supports it, otherwise by
// quitting the TUI and attaching in place. Bound to `$`; also the Enter fallback
// when an island has no attachable agents.
func (m tuiModel) openIslandShell(name string) (tea.Model, tea.Cmd) {
	if canOpenNewWindow() {
		if err := m.openAgentWindow("shell", name, "", "", nil); err != nil {
			m.lastError = err.Error()
		}
		return m, nil
	}
	m.connectShell = name
	return m, tea.Quit
}

// attachableAgentIDs returns the island's attachable (non-headless) agent ids.
func (m tuiModel) attachableAgentIDs(name string) []string {
	isl, ok := m.islandByName(name)
	if !ok {
		return nil
	}
	var ids []string
	for _, a := range isl.Agents {
		if a.Attachable {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// openAgentLogs opens a headless agent's logs in a new window, or points the
// user at the CLI when no new-window backend is available.
func (m tuiModel) openAgentLogs(name, agentID string) (tea.Model, tea.Cmd) {
	if canOpenNewWindow() {
		if err := m.openAgentLogsWindow(name, agentID, ""); err != nil {
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
	styleNeedsYou  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24")).Bold(true) // the one call-to-action state — bold so it pops out of a quiet fleet
	styleFooter    = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8"))
	// styleBroadcast is the attention bar for the header's top line: amber
	// background, near-black bold text. Amber, not red — an update is attention,
	// not danger; red stays reserved for PANIC/errors so the two never blur.
	styleBroadcast = lipgloss.NewStyle().Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#fbbf24")).Bold(true)
	// styleErrorBroadcast is the attention bar for a failed action (e.g. a
	// self-update that errored) — red where styleBroadcast is amber, so a failure
	// reads as a failure, not just news.
	styleErrorBroadcast = lipgloss.NewStyle().Foreground(lipgloss.Color("#fef2f2")).Background(lipgloss.Color("#b91c1c")).Bold(true)
	// styleSuccessBroadcast (green) confirms an update landed cleanly. It's the
	// only broadcast that self-fades — a brief "done", not standing news.
	styleSuccessBroadcast = lipgloss.NewStyle().Foreground(lipgloss.Color("#f0fdf4")).Background(lipgloss.Color("#16a34a")).Bold(true)
	// styleWarnBroadcast (orange) flags an update that needs the user to act
	// (restart to apply). Sticky — distinct from the amber "update available"
	// prompt (news) and the red failure, so attention ≠ done ≠ broken.
	styleWarnBroadcast = lipgloss.NewStyle().Foreground(lipgloss.Color("#1a1205")).Background(lipgloss.Color("#fb923c")).Bold(true)
	// styleMenuBox frames the per-row action popup — a brighter border than the
	// panes so it reads as a modal floating above them.
	styleMenuBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#3b5b8f")).Padding(0, 2)
)

func (m tuiModel) View() string {
	if m.width == 0 {
		return "loading…"
	}

	header := m.renderHeader()
	// PANIC overrides everything — show it first, in alarm styling.
	if banner := m.renderPanicBanner(); banner != "" {
		header = lipgloss.JoinVertical(lipgloss.Left, header, banner)
	}
	// Substrate warning: the Docker VM is too small for the host, so islands will
	// OOM no matter what per-island knobs you set (#23). Sits below PANIC.
	if banner := m.renderVMBanner(); banner != "" {
		header = lipgloss.JoinVertical(lipgloss.Left, header, banner)
	}
	// The update broadcast now lives inside the header (its top line / a compact
	// chip), so body sizing via header height accounts for it automatically.
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
	if m.menu != nil {
		// Centered popup over an empty field — same modal treatment as the other
		// overlays, but compact (a bordered box centered in the body area).
		box := styleMenuBox.Render(m.renderActionMenu())
		body := lipgloss.Place(m.width-2, m.height-hh-2, lipgloss.Center, lipgloss.Center, box)
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.confirm != nil {
		// Same centered-modal treatment as the menu — a destructive confirm
		// (purge / force-purge / remove-agent) must be unmissable, not a thin
		// footer line that scrolls off the bottom.
		box := styleMenuBox.Render(m.renderConfirm())
		body := lipgloss.Place(m.width-2, m.height-hh-2, lipgloss.Center, lipgloss.Center, box)
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.settings != nil {
		box := styleMenuBox.Render(m.renderSettings())
		body := lipgloss.Place(m.width-2, m.height-hh-2, lipgloss.Center, lipgloss.Center, box)
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.resEditor != nil {
		box := styleMenuBox.Render(m.renderResourceEditor())
		body := lipgloss.Place(m.width-2, m.height-hh-2, lipgloss.Center, lipgloss.Center, box)
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.modelEditor != nil {
		box := styleMenuBox.Render(m.renderModelEditor())
		body := lipgloss.Place(m.width-2, m.height-hh-2, lipgloss.Center, lipgloss.Center, box)
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.audit != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderAuditView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.grants != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderGrantsView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.scope != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderScopeView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.approvals != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderApprovalsView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}

	footer := m.renderFooter()
	// The pinned host-terminal band sits between the header and the island list;
	// the body sizes off (header + band) height so nothing is pushed off-screen.
	band, bandH := m.renderBand(m.width - 2)
	body := m.renderBody(hh + bandH)
	if band != "" {
		return lipgloss.JoinVertical(lipgloss.Left, header, band, body, footer)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// renderPanicBanner returns an alarm banner while the daemon is in panic mode
// (every island stopped, auto-restart blocked), or "" otherwise.
func (m tuiModel) renderPanicBanner() string {
	if m.overview == nil || !m.overview.Panicked {
		return ""
	}
	msg := " ⛔ PANIC — all islands stopped, auto-restart blocked   ·   run: dejima panic --clear "
	if w := m.width - 2; w > 0 {
		return styleErrored.Width(w).Render(msg)
	}
	return styleErrored.Render(msg)
}

// renderVMBanner warns when the container runtime's VM is far smaller than the
// host — the substrate-level cause of island OOMs (#23). Amber (attention), not
// red (PANIC). Uses the daemon-reported host/VM figures so it's correct even
// when the TUI runs on a different machine than the daemon.
func (m tuiModel) renderVMBanner() string {
	o := m.overview
	if o == nil || !vmmem.Undersized(o.HostMemoryBytes, o.VMMemoryBytes) {
		return ""
	}
	msg := fmt.Sprintf(" ⚠ Docker VM has %s of %s host RAM — islands share this whole pool and will OOM. Fix: dejima doctor --fix   ·   or: colima start --memory %d ",
		humanBytes(o.VMMemoryBytes), humanBytes(o.HostMemoryBytes), o.VMRecommendedBytes>>30)
	if w := m.width - 2; w > 0 {
		return styleBroadcast.Width(w).Render(msg)
	}
	return styleBroadcast.Render(msg)
}

// updateParts returns the "client X→Y · daemon X→Y" fragment naming whatever is
// behind the latest release, or "" when up to date. Broadcast in the header's top
// line (full mode) and as a chip on the right (compact). [U] applies whichever is
// behind — a client self-update (replace this binary), a daemon self-update
// (operator endpoint → daemon updates and restarts), or, with both stale, client
// then daemon. (Earlier this hinted "run: dejima update", which is wrong for a
// daemon-only update from a remote client — that command updates the client.)
func (m tuiModel) updateParts() string {
	var parts []string
	if m.clientUpdate {
		parts = append(parts, fmt.Sprintf("client %s→%s", version.Version, m.latestRelease))
	}
	if m.daemonUpdate && m.overview != nil {
		parts = append(parts, fmt.Sprintf("daemon %s→%s", m.overview.DaemonVersion, m.latestRelease))
	}
	return strings.Join(parts, " · ")
}

// announcement is the header's single broadcast slot: the most important thing to
// tell the user right now, or ok=false when there's nothing. It's deliberately a
// general slot — today the only source is an available update, but other
// transient, attention-worthy state (skew warnings, daemon notices) should reuse
// it rather than grow a new banner each time. `full` fills the top line in the
// roomy header; `short` is the chip for the compact one. Highest priority wins;
// PANIC stays its own override (renderPanicBanner) since it supersedes the UI.
func (m tuiModel) announcement() (full, short string, style lipgloss.Style, ok bool) {
	switch {
	case len(m.pendingActions) > 0:
		// Cross-island actions awaiting your decision — the headline safety moment,
		// so it outranks update news. Red + loud when any pending action is
		// destructive (which the gate never auto-approves), amber otherwise.
		n := len(m.pendingActions)
		st, tail := styleBroadcast, "await approval"
		for _, a := range m.pendingActions {
			if a.Tier == link.TierDestructive {
				st, tail = styleErrorBroadcast, "need approval — destructive!"
				break
			}
		}
		return fmt.Sprintf(" ⚖ %d cross-island action(s) %s   ·   [V] review", n, tail),
			fmt.Sprintf(" ⚖ %d to approve ", n), st, true
	case m.updateError != "":
		// A failed self-update outranks everything else here and stays put (red)
		// until retried [U] or dismissed [esc] — never wiped by a poll.
		return " ⚠ " + m.updateError + "   ·   [U] retry · [esc] dismiss",
			" ⚠ update failed ", styleErrorBroadcast, true
	case m.restartPending != "":
		// An applied-but-not-yet-active update: outstanding user action, so it
		// sticks (orange) until they restart or dismiss it.
		return " ⟳ " + m.restartPending + "   ·   [esc] dismiss",
			" ⟳ restart to apply ", styleWarnBroadcast, true
	case m.updateApplied != "":
		// A clean landing — green, and it fades on its own (updateNoticeFadedMsg).
		return " ✓ " + m.updateApplied,
			" ✓ updated ", styleSuccessBroadcast, true
	case m.clientUpdate || m.daemonUpdate:
		return " ⬆ update available: " + m.updateParts() + "   ·   [U] update",
			" ⬆ [U] update ", styleBroadcast, true
	}
	return "", "", lipgloss.Style{}, false
}

// asciiLogo is a terminal rendering of assets/logo-transparent.png: the
// island is an annulus sector (parallel top/bottom arcs joined by angled
// sides), with a gate hanging from the bottom arc and a bridge crossing
// beneath the curved shore. Every line is padded to the same 35-column
// width (artwork is 29 cols centered with a 3-col margin each side) so it
// composes as a block.
var asciiLogo = []string{
	"         ################          ",
	"      ####              ####       ",
	"   ####                     ####   ",
	"      ##      ######      ###      ",
	"       #########  ##########       ",
	"              ##  ##               ",
	"          ######  ######           ",
	"      ####              #####      ",
}

// asciiLogoSmall is the same mark drawn at half scale (7 rows, 21 cols) for
// narrow terminals that can't fit asciiLogo beside the info lines. Artwork is
// 17 cols centered with a 2-col margin each side; every line is symmetric
// about the field center.
var asciiLogoSmall = []string{
	"    #############    ",
	"  ####         ####  ",
	"   ###         ###   ",
	"    #####   #####    ",
	"         ###         ",
	"   ######   ######   ",
	"  ##             ##  ",
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

	// Flag an env-sourced target: DEJIMA_HOST overrides any saved profile, so a
	// stale export silently wins — making that visible is the whole point.
	envNote := ""
	if m.activeSource == "env" {
		envNote = " (env)"
	}

	// Compact single-line header when the terminal can't spare the rows, or
	// is too narrow for the info lines beside even the small logo (longest
	// info line is 69 cols; small logo + chrome is 21 + 9 = 30).
	if m.height < 24 || m.width < 99 {
		title := styleTitle.Render("Dejima")
		right := styleMuted.Render(label + envNote + " ⚙ [s]")
		if _, short, style, ok := m.announcement(); ok {
			// No room for the full bar; a highlighted chip still draws the eye.
			right = style.Render(short) + " " + right
		}
		pad := m.width - lipgloss.Width(title) - lipgloss.Width(right) - 2
		if pad < 1 {
			pad = 1
		}
		return " " + title + strings.Repeat(" ", pad) + right
	}

	// Always use the small mark, regardless of width. The responsive larger logo
	// is kept (commented out) so it's trivially recoverable if we want it back;
	// the blank reference keeps it from reading as dead code to the linter.
	var _ = asciiLogo
	logoArt := asciiLogoSmall
	// logoArt := asciiLogo
	// if m.width < 113 {
	// 	logoArt = asciiLogoSmall
	// }

	logoLines := make([]string, len(logoArt))
	for i, l := range logoArt {
		logoLines[i] = styleAccent.Render(l)
	}
	logo := strings.Join(logoLines, "\n")

	// server: <label>  [·  ssh <addr>]  ·  [s] switch  ·  [?] all keys
	// The ssh hint appears only when the daemon has the SSH-façade listener on
	// (--ssh); `dejima ssh config <island> --install` resolves the full address.
	serverLine := styleMuted.Render("server: ") + styleAccent.Render(label)
	if m.activeSource == "env" {
		serverLine += styleMuted.Render(" via $DEJIMA_HOST")
	}
	if m.overview != nil && m.overview.SSHAddr != "" {
		serverLine += styleMuted.Render("  ·  ssh ") + styleAccent.Render(m.overview.SSHAddr)
	}
	serverLine += styleMuted.Render("  ·  [s] settings  ·  [?] all keys")

	infoW := m.width - lipgloss.Width(logoArt[0]) - 9

	// The top line is the announcement bar: normally blank (keeping the 7-row
	// info block aligned with the logo), but a full-width highlighted broadcast
	// when there's something to say (an available update, today).
	topLine := ""
	if full, _, style, ok := m.announcement(); ok {
		topLine = style.Width(infoW).Render(full)
	}

	info := strings.Join([]string{
		topLine,
		styleTitle.Render("Dejima") + styleMuted.Render(" — isolated islands for AI coding agents, on your own hardware"),
		"",
		styleMuted.Render("Each island is a repo in its own container — host one or more agents, or just shell in."),
		styleAccent.Render("↑/↓") + styleMuted.Render(" pick  ·  ") + styleAccent.Render("⏎") + styleMuted.Render(" open its agents  ·  ") + styleAccent.Render("$") + styleMuted.Render(" shell  ·  ") + styleAccent.Render("n") + styleMuted.Render(" launch a new one"),
		styleMuted.Render("Close the terminal — agents keep running; reattach from any device."),
		serverLine,
	}, "\n")
	info = lipgloss.NewStyle().MaxWidth(infoW).Render(info)

	box := lipgloss.JoinHorizontal(lipgloss.Top, logo, "   ", info)
	return stylePane.Width(m.width - 2).Render(box)
}

func (m tuiModel) renderBody(headerHeight int) string {
	// The island/agent list is the information-dense star of the dashboard, so
	// give it the larger share (~4/7) — a full agent row (name + type + uptime +
	// state) runs wide. The detail pane keeps a floor so it stays readable; on a
	// narrow terminal the detail floor wins and the list gives way.
	leftW := m.width * 4 / 7
	rightW := m.width - leftW - 4
	if rightW < 28 {
		rightW = 28
		leftW = m.width - rightW - 4
	}
	if leftW < 30 {
		leftW = 30
	}
	// -5 = 3 footer lines (health strip + two key-hint rows) + the body pane's
	// top/bottom border.
	bodyHeight := m.height - headerHeight - 5
	if bodyHeight < 5 {
		bodyHeight = 5
	}
	innerH := bodyHeight - 2 // pane content area, minus the top+bottom border

	// Both panes are windowed to innerH lines so they can never grow taller than
	// the screen and push the header above the fold. The list follows the cursor;
	// the detail panel scrolls with PgUp/PgDn (m.detailScroll). MaxHeight is a
	// belt-and-suspenders clip in case a content line wraps.
	listContent, selLine := m.renderList(leftW - 4)
	listView := followWindow(listContent, innerH, selLine)
	detailView, _ := scrollWindow(m.renderDetail(rightW-4), innerH, m.detailScroll)

	left := stylePane.Width(leftW).Height(bodyHeight).MaxHeight(bodyHeight).Render(listView)
	right := stylePane.Width(rightW).Height(bodyHeight).MaxHeight(bodyHeight).Render(detailView)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	return body
}

// scrollWindow returns the innerH-line slice of content at the given offset,
// plus the maximum offset. When content fits, it's returned unchanged (maxOff 0).
// When it doesn't, one line is reserved for a "↕ a–b of n" position hint.
func scrollWindow(content string, innerH, offset int) (string, int) {
	if innerH <= 0 {
		return "", 0
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= innerH {
		return content, 0
	}
	visN := innerH - 1 // reserve the last line for the position hint
	if visN < 1 {
		visN = 1
	}
	maxOff := len(lines) - visN
	if offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		offset = 0
	}
	hint := fmt.Sprintf("  ↕ %d–%d of %d (PgUp/PgDn)", offset+1, offset+visN, len(lines))
	return strings.Join(lines[offset:offset+visN], "\n") + "\n" + styleMuted.Render(hint), maxOff
}

// followWindow windows content to innerH lines, scrolled so selLine stays
// visible — the list viewport that follows the cursor (no scroll keys needed).
func followWindow(content string, innerH, selLine int) string {
	if innerH <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= innerH {
		return content
	}
	visN := innerH - 1
	if visN < 1 {
		visN = 1
	}
	offset := 0
	if selLine >= visN { // keep the selection on the last visible row
		offset = selLine - visN + 1
	}
	if maxOff := len(lines) - visN; offset > maxOff {
		offset = maxOff
	}
	hint := fmt.Sprintf("  ↕ %d–%d of %d", offset+1, offset+visN, len(lines))
	return strings.Join(lines[offset:offset+visN], "\n") + "\n" + styleMuted.Render(hint)
}

// bodyInnerHeight is the per-pane content height — what scrollWindow/followWindow
// window to. Mirrors the arithmetic in renderBody so the scroll-key handler can
// clamp against the same bound the renderer uses.
func (m tuiModel) bodyInnerHeight() int {
	bodyHeight := m.height - lipgloss.Height(m.renderHeader()) - 5
	if bodyHeight < 5 {
		bodyHeight = 5
	}
	return bodyHeight - 2
}

// scrollDetail moves the detail panel by `pages` (±1), clamped to content.
func (m tuiModel) scrollDetail(pages int) tuiModel {
	innerH := m.bodyInnerHeight()
	_, maxOff := scrollWindow(m.renderDetail(0), innerH, 0)
	step := innerH - 1
	if step < 1 {
		step = 1
	}
	m.detailScroll += pages * step
	if m.detailScroll > maxOff {
		m.detailScroll = maxOff
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
	return m
}

func (m tuiModel) renderList(width int) (string, int) {
	if len(m.islands) == 0 {
		if m.lastError != "" {
			if m.daemonHelp != nil {
				return renderDaemonHelp(*m.daemonHelp), -1
			}
			return styleErrored.Render("error: "+m.lastError) + "\n\n" + styleMuted.Render("(daemon unreachable?)"), -1
		}
		body := styleMuted.Render("no islands yet\n\n`q` to quit, then `dejima init --repo <url>`")
		// Nudge missing Claude creds before the first island, so claude-code/codex
		// agents don't start unauthenticated and fail at first attach.
		if m.setupChecked && !m.claudeSeeded {
			body += "\n\n" + styleWaiting.Render("⚠ no Claude credentials yet — run `dejima auth push` (from a machine where\n  `claude` is logged in) so claude-code/codex agents start authenticated.")
		}
		return body, -1
	}

	byName := make(map[string]api.IslandInfo, len(m.islands))
	for _, isl := range m.islands {
		byName[isl.Name] = isl
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render("Islands"))
	b.WriteString("\n\n")
	selLine := -1
	lastRepo := "\x00" // sentinel so the first group always prints its header
	for i, row := range m.visibleRows() {
		// Grouped view: a muted repo header before each repo group's first island.
		// Injected like the Host header — an extra line that doesn't consume a row
		// index, so the cursor mapping is unaffected.
		if m.grouped && row.kind == rowIsland {
			if isl, ok := byName[row.island]; ok && isl.Repo != lastRepo {
				label := isl.Repo
				if label == "" {
					label = "(no repo)"
				}
				b.WriteString(styleMuted.Render("◇ "+shortenRepo(label)) + "\n")
				lastRepo = isl.Repo
			}
		}
		var line string
		switch row.kind {
		case rowNewIsland:
			line = styleAccent.Render("+ new island")
		case rowAddAgent:
			// Caps the island's child group (└); agent rows above it branch (├).
			line = "   " + styleMuted.Render("└ + add agent")
		case rowAgent:
			isl := byName[row.island]
			a := agentByID(isl, row.agentID)
			line = "   " + styleMuted.Render("├ ") + agentRowText(a, labelIsAmbiguous(isl.Agents, a))
		default: // rowIsland
			isl, ok := byName[row.island]
			if !ok {
				continue
			}
			caret := "▸"
			if m.islandExpanded(isl) {
				caret = "▾"
			}
			label := truncate(islandDisplay(isl), 14)
			if len(isl.Agents) > 1 {
				label = truncate(islandDisplay(isl), 10) + fmt.Sprintf(" (%d)", len(isl.Agents))
			}
			// Per-island visual identity: a stable color+glyph (idStyle/idGlyph)
			// marks the island and tints its name, so it and its agent group stand
			// out. The state glyph (glyphFor) keeps its own status color.
			idStyle, idGlyph := islandIdentity(isl.Name)
			line = fmt.Sprintf("%s %s %s  %s  %s",
				caret, glyphFor(isl), idStyle.Render(idGlyph),
				idStyle.Render(fmt.Sprintf("%-14s", label)),
				shortStatus(isl, m.dirtyOps[isl.Name]))
		}
		if i == m.selected {
			selLine = strings.Count(b.String(), "\n") // line index this row will occupy
			line = styleSelected.Render("▶ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	// Clip each row to the pane width instead of letting lipgloss wrap it — a
	// wrapped agent row spills onto a second line, shreds the tree, and (worse)
	// throws off the selLine→viewport math below. MaxWidth is ANSI-aware, so the
	// embedded colors survive, and truncation never changes the line count, so
	// selLine stays valid.
	out := b.String()
	if width > 0 {
		out = lipgloss.NewStyle().MaxWidth(width).Render(out)
	}
	return out, selLine
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
	glyphTerminal = "❯" // a plain shell/terminal you type into
	glyphAgent    = "◆" // an AI agent you attach to (claude-code, codex, …)
	glyphHeadless = "■" // headless agent — supervised background process, logs only
)

// agentTypeShell mirrors handlers.Shell — the plain-terminal agent type. Kept as
// a local const so the TUI doesn't import internal/handlers for one string.
const agentTypeShell = "shell"

// agentStatus normalizes an agent's last emitted signal (AgentState.Latest),
// credential readiness (AuthState), session liveness (State), and any
// orchestration Error into one legible, colored word. It is the single source
// of truth for agent state across the UI — the row signal, the glyph color
// (agentGlyph), and the detail panel all read from it, so the word and the
// color can never disagree. "needs you" is the one call-to-action state,
// rendered bold (styleNeedsYou) so it pops out of a fleet of quiet agents.
//
// State and Restarts are probed only for the detail view (agentInfos live=true);
// in the island list they're empty. So liveness words (working/idle/stopped/
// crash-loop) appear only once State is known — in the list we degrade to the
// shim signal (needs you / done / error / no model key), and otherwise return
// "" with a muted style, matching the list's "state not probed" reality. An
// empty word means the row shows no state token and the glyph stays neutral.
func agentStatus(a api.AgentInfo) (string, lipgloss.Style) {
	latest := ""
	if a.AgentState != nil {
		latest = a.AgentState.Latest
	}
	switch {
	case a.Error != "" || latest == "error":
		return "error", styleErrored
	case a.State == "exited":
		return "exited", styleErrored // session alive but the agent process died
	case latest == "waiting-for-input":
		return "needs you", styleNeedsYou
	case a.AuthState == "missing-provider-auth":
		return "no model key", styleWaiting // will fail at first task — flag it
	case a.State == "running" && a.Restarts >= 3:
		return "crash-loop", styleWaiting // supervised but crash-looping (e.g. OOM)
	case a.State == "stopped":
		return "stopped", styleHibernate
	case a.State == "running" && latest == "task-complete":
		return "idle", styleHibernate
	case a.State == "running":
		return "working", styleRunning
	case latest == "task-complete":
		return "done", styleHibernate // list view: liveness unknown, last signal was done
	default:
		return "", styleHibernate // unknown (list, no signal yet) — neutral glyph, no word
	}
}

// agentGlyph renders an agent's kind glyph colored by its state: the shape says
// terminal vs AI-agent vs headless (stable identity); the color comes from
// agentStatus, so the glyph and the row's state word always agree.
func agentGlyph(a api.AgentInfo) string {
	g := glyphAgent // attachable AI agents (claude-code, codex, custom interactive)
	switch {
	case a.Type == agentTypeShell:
		g = glyphTerminal // a plain shell you type into
	case !a.Attachable:
		g = glyphHeadless // background process
	}
	_, style := agentStatus(a)
	return style.Render(g)
}

// agentDisplayName is the human-facing name for an agent: its user-given label
// if set, else its id (p1/p2/…). The id is also the addressing handle (it still
// leads the detail view), so an unlabeled agent shows that handle rather than a
// generic type name. See [agentRowText] / renderAgentDetail.
func agentDisplayName(a api.AgentInfo) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

// terminalRowText renders one host-terminal row: terminal glyph, name (label or
// id), and the muted id handle.
func terminalRowText(t hostterm.Terminal) string {
	name := t.Label
	if name == "" {
		name = t.ID
	}
	return fmt.Sprintf("%s %-14s %s", glyphTerminal, truncate(name, 14), styleMuted.Render(t.ID))
}

// bandRowCount is the number of selectable rows in the expanded band: one per
// terminal, plus the trailing "+ new terminal" affordance.
func (m tuiModel) bandRowCount() int { return len(m.terminals) + 1 }

// renderBand draws the pinned host-terminal band that sits above the island
// list, and returns it with its height in lines (0 when host terminals are off,
// so the caller adds no rows). Collapsed it's a single summary line; focused it
// expands to the terminal list + a "+ new terminal" row, with bandSel
// highlighted. Rows are clipped (never wrapped) to width, like the island list.
func (m tuiModel) renderBand(width int) (string, int) {
	if !m.hostTerminalsEnabled() {
		return "", 0
	}
	n := len(m.terminals)
	clip := func(s string) string {
		if width > 0 {
			return lipgloss.NewStyle().MaxWidth(width).Render(s)
		}
		return s
	}

	if !m.bandExpanded {
		dot := styleMuted.Render("○")
		count := "no terminals"
		if n > 0 {
			dot = styleRunning.Render("●")
			s := ""
			if n != 1 {
				s = "s"
			}
			count = fmt.Sprintf("%d terminal%s", n, s)
		}
		line := fmt.Sprintf("%s %s %s %s   %s",
			styleHeader.Render("⌨ Host"), dot, styleMuted.Render(count),
			styleMuted.Render("· not contained"), styleMuted.Render("[`] expand"))
		return clip(line), 1
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render("⌨ Host terminals") + " " +
		styleMuted.Render("· not contained") + "   " + styleMuted.Render("[`] collapse") + "\n")
	for i, t := range m.terminals {
		line := "  " + terminalRowText(t)
		if i == m.bandSel {
			line = styleSelected.Render("▶ " + terminalRowText(t))
		}
		b.WriteString(clip(line) + "\n")
	}
	newRow := "  " + styleMuted.Render("+ new terminal")
	if m.bandSel == n {
		newRow = styleSelected.Render("▶ + new terminal")
	}
	b.WriteString(clip(newRow))
	// height = header + n terminal rows + the new-terminal row
	return b.String(), n + 2
}

// bandKey drives the focused host-terminal band: navigate the terminals + the
// "+ new terminal" row, attach (⏎), create, close (d/X), relabel (e), and
// collapse-on-blur (esc / backtick). Reuses the same commands as the old inline
// Host rows, so terminal behavior is unchanged — only its home moved.
func (m tuiModel) bandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.terminals)
	collapse := func() (tea.Model, tea.Cmd) {
		m.bandExpanded, m.bandFocused = false, false
		return m, nil
	}
	switch msg.String() {
	case "esc", "`", "left", "q":
		return collapse()
	case "j", "down":
		if m.bandSel < m.bandRowCount()-1 {
			m.bandSel++
		}
		return m, nil
	case "k", "up":
		if m.bandSel > 0 {
			m.bandSel--
		}
		return m, nil
	case "g", "home":
		m.bandSel = 0
		return m, nil
	case "G", "end":
		m.bandSel = m.bandRowCount() - 1
		return m, nil
	case "enter", "o":
		if m.bandSel >= n { // the "+ new terminal" row
			return m, m.createTerminalCmd("")
		}
		m.connectTerminal = m.terminals[m.bandSel].ID // attach (resumes live tmux)
		return m, tea.Quit
	case "d", "X":
		if m.bandSel < n {
			m.confirm = &confirmPrompt{verb: "remove-terminal", agent: m.terminals[m.bandSel].ID}
		}
		return m, nil
	}
	return m, nil
}

// agentRowText renders one agent's list line: kind glyph, name (label, or id
// when unlabeled), a muted meta cluster (type, uptime, a presence dot), then the
// normalized state word (agentStatus), colored to match the glyph. The id isn't
// repeated when a label is present — it stays available in the detail view —
// unless ambiguous is set, meaning another agent in the same island shares this
// display name, in which case the muted id is appended so the two rows aren't
// indistinguishable.
func agentRowText(a api.AgentInfo, ambiguous bool) string {
	name := fmt.Sprintf("%-14s", truncate(agentDisplayName(a), 14))
	if ambiguous {
		// rare: append the disambiguating id. The trailing columns shift for
		// these rows, which is fine — they're deliberately distinct.
		name = truncate(agentDisplayName(a), 10) + " " + styleMuted.Render(a.ID)
	}
	// Muted meta: the agent type, plus uptime/age unless the session is known to
	// be down. (State is unprobed in the list, so we show age there too; the
	// state word, not this, is what says whether it's actually running.)
	meta := a.Type
	if a.State != "stopped" && a.State != "exited" && !a.CreatedAt.IsZero() {
		meta += "  up " + timeAgo(a.CreatedAt)
	}
	metaStr := styleMuted.Render(meta)
	if v := attachedIndicator(a.Attached); v != "" {
		metaStr += "  " + v
	}
	status, statusStyle := agentStatus(a)
	statusStr := ""
	if status != "" {
		statusStr = "  " + statusStyle.Render(status)
	}
	return fmt.Sprintf("%s %s  %s%s", agentGlyph(a), name, metaStr, statusStr)
}

// attachedIndicator renders a compact "someone's driving this" badge — a
// presence dot plus the viewer count when more than one client is attached. It
// stays empty (and silent) when nobody's watching, so a quiet fleet reads
// quiet. The detail panel lists who and for how long; this is the at-a-glance
// cue. Rendered in accent so it's noticeable without competing with the amber
// "needs you" state.
func attachedIndicator(attached []api.PresenceEntry) string {
	switch n := len(attached); n {
	case 0:
		return ""
	case 1:
		return styleAccent.Render("◉")
	default:
		return styleAccent.Render(fmt.Sprintf("◉%d", n))
	}
}

// labelIsAmbiguous reports whether another agent in the same island renders to
// the same display name as a — used to decide whether a row needs its id handle
// appended to stay distinguishable.
func labelIsAmbiguous(agents []api.AgentInfo, a api.AgentInfo) bool {
	name := agentDisplayName(a)
	n := 0
	for _, other := range agents {
		if agentDisplayName(other) == name {
			n++
		}
	}
	return n > 1
}

func (m tuiModel) renderDetail(_ int) string {
	// The trailing "+ new island" row has no island behind it.
	if m.currentRow().kind == rowNewIsland {
		return styleTitle.Render("+ New island") + "\n\n" +
			styleMuted.Render("Press ⏎ to pick a repo and an agent, then launch.")
	}
	if m.currentRow().kind == rowAddAgent {
		return styleTitle.Render("+ Add agent") + "\n\n" +
			styleMuted.Render("Press ⏎ to add an agent to "+styleAccent.Render(m.selectedName())+styleMuted.Render(".\nClaude Code, Codex, a terminal, or a headless command."))
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
	if d.Owner != "" {
		b.WriteString(fmt.Sprintf("owner:     %s\n", styleMuted.Render(d.Owner)))
	}
	if len(d.Tags) > 0 {
		b.WriteString(fmt.Sprintf("tags:      %s\n", styleMuted.Render(formatTags(d.Tags))))
	}
	if d.Stats != nil {
		b.WriteString(fmt.Sprintf("memory:    %s / %s\n",
			humanBytes(d.Stats.MemoryUsageBytes), humanBytes(d.Stats.MemoryLimitBytes)))
		b.WriteString(fmt.Sprintf("cpu:       %.1f%%\n", d.Stats.CPUPercent))
	}
	if d.Disk != nil && d.Disk.TotalBytes > 0 {
		b.WriteString(fmt.Sprintf("disk:      %s (ws %s · home %s)\n",
			humanBytes(uint64(d.Disk.TotalBytes)), humanBytes(uint64(d.Disk.WorkspaceBytes)),
			humanBytes(uint64(d.Disk.HomeBytes))))
	}
	if r := d.Resources; r != nil {
		// Lead with OOM priority (the meaningful knob); show a memory cap only when
		// one is actually set — "unlimited" is the default and just adds noise.
		line := "priority " + oomTierLabel(r.OOMPriority)
		if r.Memory != "" {
			line += " · mem cap " + r.Memory
		}
		b.WriteString(fmt.Sprintf("limits:    %s   %s\n", line, styleMuted.Render("(m → Resources…)")))
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
	// SSH connect info — shown when the daemon's SSH-façade is enabled. The host
	// is resolved once per overview refresh (see overviewMsg); editor setup is a
	// one-liner via `dejima ssh config`.
	if m.overview != nil && m.overview.SSHAddr != "" && m.sshHost != "" {
		b.WriteString(fmt.Sprintf("ssh:       %s  (%s@%s -p %s)\n",
			styleAccent.Render("dejima-"+d.Name), d.Name, m.sshHost, m.sshPort))
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
			b.WriteString("  " + agentRowText(a, labelIsAmbiguous(d.Agents, a)) + "\n")
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
	if sw, ss := agentStatus(a); sw != "" {
		b.WriteString(fmt.Sprintf("state:     %s\n", ss.Render(sw)))
	}
	state := a.State
	switch a.State {
	case "":
		state = "—"
	case "exited":
		state = styleErrored.Render("exited — agent process died (shell prompt remains)")
	}
	b.WriteString(fmt.Sprintf("session:   %s\n", state))
	if a.Restarts > 0 {
		note := fmt.Sprintf("%d (supervised — auto-restarts on crash)", a.Restarts)
		if a.Restarts >= 3 {
			note = styleWaiting.Render(fmt.Sprintf("%d ⚠ crash-looping — check logs (likely OOM)", a.Restarts))
		}
		b.WriteString(fmt.Sprintf("restarts:  %s\n", note))
	}
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
	if a.Model != "" {
		b.WriteString(fmt.Sprintf("model:     %s\n", a.Model))
	}
	if a.AuthState == "missing-provider-auth" {
		prov := a.Provider
		if prov == "" {
			prov = "a provider"
		}
		b.WriteString("auth:      " + styleWaiting.Render("⚠ no API key for "+prov+" — press [v] to set the model + key") + "\n")
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
	// Row 1: globals. Row 2: navigation + the ⏎ action menu, which now holds the
	// per-row lifecycle/setup actions (hibernate, reset, purge, rename, ssh setup,
	// …) instead of crowding the bar. Those keys still work directly; they're
	// listed in the ⏎ menu and in [?] help.
	term := ""
	if m.hostTerminalsEnabled() {
		term = "[`] terminals   "
	}
	keys1 := "[n] new   " + term + "[s] settings   [?] help   [q] quit"
	keys2 := "[⏎] open   [m] actions   [space] expand   " + expandAll
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
	if m.lastNotice != "" {
		return styleRunning.Render("✓ " + truncate(m.lastNotice, 76))
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
		{"t", "new host terminal — an uncontained shell on the daemon host (if enabled)"},
		{"⏎ / o", "island → opens all its agents (each in a new tab); agent → its session; headless agent → its logs"},
		{"$", "open a shell at /workspace inside the highlighted island (contained)"},
		{"m", "actions menu for the highlighted row (attach, hibernate, rename, ssh setup, purge…)"},
		{"space ←/→", "expand an island to its agents, the + add-agent row, and headless logs"},
		{"E", "expand / collapse all islands at once (flips on the current state)"},
		{"p", "group the island list by repo — multi-agent projects read as one"},
		{"+", "add an agent — Claude Code, Codex, a terminal, or a headless command"},
		{"e", "rename — island display title, or relabel an agent (cosmetic; the slug/id stay)"},
		{"[ ]", "reorder the highlighted agent within its island (move up / down)"},
		{"a", "attach here instead — replaces the dashboard with the agent"},
		{"↑/↓ j/k", "move between rows   ·   g/G jump to top/bottom"},
		{"PgUp/PgDn", "scroll the detail panel (events, agents) — Ctrl-u/Ctrl-d also work"},
		{"Ctrl-b d", "detach from a session — the agent keeps running inside"},
		{"Ctrl-\\", "from inside a session: summon this dashboard (with the terminal band) — session stays alive"},
		{"q", "quit the dashboard"},
	}
	for _, kv := range basic {
		b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(fmt.Sprintf("%-9s", kv[0])), styleMuted.Render(kv[1])))
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("An island = a contained workspace that can hold several agents sharing its\ncreds and git. ⏎ on an island opens all its agents (each in its own window); ⏎\non an agent opens just that one; $ opens a shell at /workspace (inside the\ncontainer). Expand one with [space], then [+] add agents. Headless agents have\nno screen — ⏎ opens their logs."))
	b.WriteString("\n\n")
	b.WriteString(styleHeader.Render("Glyphs"))
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render(fmt.Sprintf(
		"%s island   %s AI agent   %s shell   %s headless   ", "●", glyphAgent, glyphTerminal, glyphHeadless)) +
		styleAccent.Render("◉") + styleMuted.Render(" attached (someone's driving)"))
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render("color = state: ") +
		styleRunning.Render("working") + styleMuted.Render(" · ") +
		styleHibernate.Render("idle/stopped") + styleMuted.Render(" · ") +
		styleNeedsYou.Render("needs you") + styleMuted.Render(" · ") +
		styleErrored.Render("error"))
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render("each island also has its own stable color + glyph (e.g. ") +
		func() string { st, g := islandIdentity("alpha"); return st.Render(g + " name") }() +
		styleMuted.Render(") so it's recognizable at a glance"))
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
		{"c", "open the island in your editor over SSH, straight at /workspace"},
		{"s", "settings — editor · group-by-repo · connection target (server)"},
		{"p", "toggle group-by-repo (also in settings)"},
		{"A", "audit ledger — chain-verification + recent governance activity"},
		{"T", "grants — what the highlighted island can reach (Port · MCP · links · caps)"},
		{"P", "Port scopes — brokered host-file grants (add/revoke; deny-all by default)"},
		{"V", "approvals — review/approve/deny pending cross-island actions (the action gate)"},
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
	case "recreate-island":
		prompt = fmt.Sprintf("OOM priority changed — restart %q now to apply? (recreates the container; workspace + agents preserved) Type 'y' and Enter: %s",
			c.island, c.answer)
	case "build-image":
		prompt = fmt.Sprintf("Rebuild the island image? Takes a few minutes; islands pick it up on upgrade. Type 'y' and press Enter: %s",
			c.answer)
	case "purge":
		prompt = fmt.Sprintf("DESTROY %q (including all volumes). Type the island name to confirm: %s",
			c.island, c.answer)
	case "force-purge":
		prompt = fmt.Sprintf("%q has unpushed/uncommitted work that will be LOST. Force-purge anyway? Type 'y' and Enter: %s",
			c.island, c.answer)
	case "remove-agent":
		who := c.agent
		if isl, ok := m.islandByName(c.island); ok {
			if lbl := agentByID(isl, c.agent).Label; lbl != "" {
				who = lbl
			}
		}
		prompt = fmt.Sprintf("Remove agent %q (id %s) from island %q — destroys its worktree + agent state. Type the agent id %q to confirm: %s",
			who, c.agent, c.island, c.agent, c.answer)
	case "remove-terminal":
		prompt = fmt.Sprintf("Close host terminal %s (kills the shell on the daemon host)? Type 'y' and press Enter: %s",
			c.agent, c.answer)
	case "approve-action":
		prompt = fmt.Sprintf("⚠ Approve this DESTRUCTIVE cross-island action (%s)? It runs once approved. Type 'y' and press Enter: %s",
			c.agent, c.answer)
	case "deny-action":
		prompt = fmt.Sprintf("Deny action %s. Reason (optional) — type one and press Enter, or just Enter: %s",
			c.agent, c.answer)
	case "approve-rule":
		prompt = fmt.Sprintf("Approve %s AND auto-approve this link+action going forward. Type '<max> [<ttl>]' (e.g. '20 1h'; blank = unlimited, no expiry) and Enter: %s",
			c.agent, c.answer)
	case "open-all-agents":
		prompt = fmt.Sprintf("Open all %d agents of %q in separate windows? Type 'y' and press Enter: %s",
			len(m.attachableAgentIDs(c.island)), c.island, c.answer)
	case "relabel-agent":
		prompt = fmt.Sprintf("Rename agent %s (blank clears the label). Type a name and press Enter: %s",
			c.agent, c.answer)
	case "rename-island":
		prompt = fmt.Sprintf("Rename %q (display title; blank resets to the name). Type a title and press Enter: %s",
			c.island, c.answer)
	case "setup-ssh":
		prompt = fmt.Sprintf("Authorize this machine's SSH key for ALL islands and add ~/.ssh/config entries for VS Code/Cursor? Type 'y' and Enter: %s",
			c.answer)
	case "update-client":
		prompt = fmt.Sprintf("Download %s and replace this dejima binary (verified against the release checksums)? Type 'y' and Enter: %s",
			m.latestRelease, c.answer)
	case "update-daemon":
		prompt = fmt.Sprintf("Update the daemon to %s and restart it (briefly disconnects)? Type 'y' and Enter: %s",
			m.latestRelease, c.answer)
	}
	// Render inside the centered styleMenuBox (View supplies the border): a clear
	// title, the prompt with a blinking-style cursor on the typed answer, and a
	// key hint — so the confirm is an unmissable pop-up, not a one-line footer.
	title := styleHeader.Render("Confirm")
	switch c.verb {
	case "purge", "force-purge", "remove-agent", "remove-terminal":
		title = styleErrored.Render("⚠  Confirm")
	}
	hint := styleHeader.Render("Enter = confirm    ·    Esc = cancel")
	return title + "\n\n" + prompt + "▌" + "\n\n" + hint
}

// renderActionMenu draws the inner content of the per-row context popup: a
// title, the gated items (selected row highlighted, destructive ones in alarm
// color), and a key hint. styleMenuBox supplies the border.
func (m tuiModel) renderActionMenu() string {
	am := m.menu
	var b strings.Builder
	b.WriteString(styleHeader.Render(am.title))
	b.WriteString("\n\n")
	for i, it := range am.items {
		mark := "   "
		if i == am.sel {
			mark = styleAccent.Render(" ▸ ")
		}
		st := lipgloss.NewStyle()
		switch {
		case i == am.sel:
			st = styleSelected
		case it.danger:
			st = styleErrored
		}
		b.WriteString(mark + st.Render(it.label) + styleMuted.Render("  ["+it.key+"]") + "\n")
	}
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("↑/↓ move · ⏎ select · esc close"))
	return b.String()
}

// renderSettings draws the general-settings overlay — either the top
// preferences list or the editor radio sub-page.
func (m tuiModel) renderSettings() string {
	st := m.settings
	var b strings.Builder
	row := func(i int, mark, text string) {
		lead := "   "
		style := lipgloss.NewStyle()
		if i == st.sel {
			lead = styleAccent.Render(" ▸ ")
			style = styleSelected
		}
		b.WriteString(lead + mark + style.Render(text) + "\n")
	}

	if st.page == settingsEditor {
		b.WriteString(styleHeader.Render("Settings · preferred editor"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("which editor 'c' opens an island in (Remote-SSH, at /workspace)"))
		b.WriteString("\n\n")
		for i, c := range editorChoices {
			dot := "○ "
			if c.cmd == m.editor {
				dot = "● "
			}
			row(i, dot, c.label)
		}
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("↑/↓ move · ⏎ choose · esc back"))
		return b.String()
	}

	b.WriteString(styleHeader.Render("Settings"))
	b.WriteString("\n")
	// Version line: this client, the connected daemon (when it differs), and
	// whether anything's behind the latest release.
	ver := "dejima " + version.Version
	if m.overview != nil && m.overview.DaemonVersion != "" && m.overview.DaemonVersion != version.Version {
		ver += " · daemon " + m.overview.DaemonVersion
	}
	switch {
	case m.clientUpdate || m.daemonUpdate:
		ver += "  ·  " + styleWaiting.Render("update available → "+m.updateParts())
	case m.latestRelease != "":
		ver += "  ·  " + styleRunning.Render("up to date")
	}
	b.WriteString(styleMuted.Render(ver))
	b.WriteString("\n\n")

	editorLabel := editorChoices[editorIndex(m.editor)].label
	groupState := "off"
	if m.grouped {
		groupState = "on"
	}
	target := m.activeLabel
	if target == "" {
		target = "local"
	}
	updateRow := "Update                    " + styleMuted.Render("up to date")
	if m.clientUpdate || m.daemonUpdate {
		updateRow = "Update                    " + styleWaiting.Render("→ "+m.latestRelease)
	}
	row(0, "", "Preferred editor          "+styleMuted.Render(editorLabel)+styleMuted.Render("  →"))
	row(1, "", "Group islands by repo     "+styleMuted.Render(groupState))
	row(2, "", "Connection target         "+styleMuted.Render(target)+styleMuted.Render("  →"))
	row(3, "", "Check for updates")
	row(4, "", updateRow)
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("↑/↓ move · ⏎ select · esc close"))
	return b.String()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// islandIdentityColors / islandIdentityGlyphs are the palette for per-island
// visual identity (a3 brief #2): a stable color + glyph per island so islands —
// and the agents grouped under them — are distinguishable at a glance (and the
// hero/containment recordings read clearly). Colors are light-medium so they
// show on both the default dark background and the selected-row highlight, and
// deliberately avoid the state hues (green/amber/red, see styleRunning/Waiting/
// Errored) so identity never reads as status. Glyphs avoid the lifecycle glyphs
// (●/⏸/◌/✱/!) for the same reason. Scope: name + 1 color + 1 glyph, no theming.
var islandIdentityColors = []lipgloss.Color{
	"#60a5fa", "#a78bfa", "#22d3ee", "#f472b6", "#2dd4bf",
	"#e879f9", "#38bdf8", "#818cf8", "#f0abfc", "#5eead4",
}

var islandIdentityGlyphs = []string{"◆", "▲", "★", "■", "◈", "✦", "♦", "⬟"}

// islandIdentity returns a stable color+glyph for an island, derived
// deterministically from its durable Name so it never changes across restarts
// (and matches between sessions/devices without any backend). Color and glyph
// are decorrelated so two islands rarely collide on both. When the backend ships
// a stored visual-identity field, prefer it and fall back to this.
func islandIdentity(name string) (lipgloss.Style, string) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	sum := h.Sum32()
	color := islandIdentityColors[sum%uint32(len(islandIdentityColors))]
	glyph := islandIdentityGlyphs[(sum/uint32(len(islandIdentityColors)))%uint32(len(islandIdentityGlyphs))]
	return lipgloss.NewStyle().Foreground(color), glyph
}

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

// timeUntil is timeAgo's forward-looking sibling: a short "in 5m" / "in 2h" for
// a future instant (or "now" once it's passed). Used for auto-approve-rule expiry.
func timeUntil(t time.Time) string {
	d := time.Until(t).Round(time.Second)
	if d <= 0 {
		return "now"
	}
	return "in " + timeAgo(time.Now().Add(-d))
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
