package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
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
	"path/filepath"
)

// newTUICmd is the interactive dashboard. Launched by `dejima` with no args.
// One-shot CLI verbs (`dejima ls`, etc.) continue to work for scripting.
func newTUICmd() *cobra.Command {
	var demo bool
	cmd := &cobra.Command{
		Use:    "tui",
		Short:  "Launch the interactive dashboard (default when run with no args).",
		Hidden: true, // not surfaced in `dejima --help`; users get it via bare `dejima`.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), demo)
		},
	}
	// --demo drives the dashboard from a synthetic fleet (no daemon) for the site
	// recordings: reproducible, secret-free, and animated. `!` stages the
	// action-gate scene. See strategy/tui-capture-runbook.md.
	cmd.Flags().BoolVar(&demo, "demo", false, "drive the dashboard from a synthetic fleet (for screen recordings; no daemon)")
	return cmd
}

// runTUI starts the bubbletea program; on Enter, it exits with a saved
// connect-to-this-island intent which the caller acts on after the TUI loop.
func runTUI(ctx context.Context, demo bool) error {
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
		m.demo = demo
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

	seededNoticeAt time.Time // timestamp of the last Claude auto-seed event surfaced (shown once, not per poll)

	// Setup-readiness snapshot (fetched once at Init) so the UI can warn about a
	// missing credential BEFORE an island is created rather than at first agent
	// attach. setupChecked guards against a false warning before the fetch lands.
	setupChecked bool
	claudeSeeded bool            // daemon can seed new islands with Claude creds
	agentKeyGap  map[string]bool // agent type → requires an LLM provider key, none configured for it

	selected int
	grouped  bool // group the island list by repo (toggled with `p`)
	// Multi-tenant ownership lens (P4, design/multi-tenant-ownership.md). callerOwner
	// / callerRole are the authenticated caller's own resolved owner id + role,
	// populated each poll from OverviewResponse (empty on a daemon predating the
	// model → filtering disabled, nothing hides). ownerLens toggles the host-owner's
	// view between just-mine and everyone's (`O`); the toggle is owner-only, since a
	// teammate's list is already owner-filtered server-side (P2).
	callerOwner string
	callerRole  string // "owner" | "operator" | "viewer" | "" (unknown)

	tipTick   int // advances each overview poll; drives the rotating header Tip line (see currentTip)
	ownerLens int // lensOwn (default) | lensAll
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
	selfStamp      string // on-disk identity of this executable at startup
	updateCheckErr string // why the last release check failed ("" = it succeeded)
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

	ticks int // tickMsg counter: drives footer-tip rotation + occasional voice re-check

	help       bool            // help overlay visible (all key sections always shown)
	helpMore   bool            // help: the collapsible reference (glyphs + CLI) is expanded
	helpScroll int             // scroll offset (lines) for the help overlay
	creator    *creatorModel   // non-nil while the new-island flow is active
	switcher   *switcherModel  // non-nil while the connection switcher is open
	agentAdder *agentAdder     // non-nil while the add-agent flow is active
	expanded   map[string]bool // island name → agents-revealed (default: all expanded)

	activeHost   string            // current target: "" = local socket, else host:port
	activeLabel  string            // profile name for the active target, if known
	activeSource string            // where the target came from: "env" | "profile" | "local"
	detailScroll int               // scroll offset (lines) for the detail panel; reset on selection change
	skew         string            // client/daemon version-skew warning, or ""
	editor       string            // preferred Remote-SSH editor CLI ("" = auto-detect); from clientcfg
	settings     *settingsModel    // non-nil while the settings overlay is open
	resEditor    *resourceEditor   // non-nil while the per-island resources overlay is open
	spawnGrant   *spawnGrantEditor // non-nil while the per-island sub-agent-budget overlay is open
	modelEditor  *modelEditor      // non-nil while the per-agent model/provider/key overlay is open
	audit        *auditView        // non-nil while the audit-ledger viewer is open (opened with `A`)
	grants       *grantsView       // non-nil while the island-grants trust view is open (opened with `T`)
	scope        *scopeView        // non-nil while the Port scope-picker is open (opened with `P`)
	approvals    *approvalsView    // non-nil while the action-gate approvals overlay is open (opened with `V`)
	identity     *identityView     // non-nil while the visual-identity editor is open (opened with `i`)
	team         *teamView         // non-nil while the owner-only Team / invite overlay is open (opened with `I`)
	github       *githubView       // non-nil while the self-serve GitHub identity pane is open (settings → GitHub)
	secretsPane  *secretsView      // non-nil while the per-island Secrets pane is open
	aggregate    *aggregateView    // non-nil while the host-utilization panel is open (opened with `%`)
	// pendingActions is the polled queue of cross-island actions awaiting approval
	// (action gate, Lane 5 P3). Drives the announcement-bar badge; empty when the
	// gate is unused/disabled. See tui_approvals.go.
	pendingActions []link.ActionRequest
	// policyRules are the active auto-approve rules, loaded when the approvals
	// overlay opens (and after a mutation) — not polled. See tui_approvals.go.
	policyRules []policy.Rule
	// demo drives the dashboard from a synthetic fleet (tui_demo.go) instead of a
	// live daemon — for reproducible, secret-free site recordings. demoTick
	// advances each poll so the fleet's agent states churn on screen.
	// demoApprovals stages the action-gate scene (pending actions + badge),
	// toggled with `!` so the hero fleet shot stays clean until you want it.
	demo          bool
	demoTick      int
	demoApprovals bool
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
	// updating is a BLUE in-progress banner shown while an update command is
	// running — a daemon source update does `git pull && make install` (tens of
	// seconds) before it restarts, and without this the TUI looks frozen between
	// the keypress and the result. Set when the update command fires, cleared by
	// its result (applied / restart-pending / error).
	updating string
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
	// global marks a menu not anchored to a list row (the Server menu [H]): its
	// items act on the host/daemon, not the highlighted island, so chooseMenuItem
	// skips the row re-anchor that per-row menus need.
	global bool
}

type actionMenuItem struct {
	label  string // human label, e.g. "Hibernate"
	key    string // the accelerator this dispatches, e.g. "h" (empty when open is set)
	danger bool   // destructive — rendered in alarm color
	// disabled greys the item out and makes it un-selectable (e.g. "daemon up to
	// date" — shown for context, but there's nothing to do).
	disabled bool
	// open, when set, is a menu-only action with no global hotkey — chooseMenuItem
	// calls it directly (after re-anchoring) instead of re-dispatching a key.
	open func(tuiModel) (tea.Model, tea.Cmd)
}

func initialTUIModel(c *api.Client) tuiModel {
	host, label, source := resolveTarget()
	cfg, _ := clientcfg.Load()
	m := tuiModel{
		client:       c,
		dirtyOps:     map[string]string{},
		expanded:     map[string]bool{},
		activeHost:   host,
		activeLabel:  label,
		activeSource: source,
		editor:       cfg.Editor,
		// Remember which copy of the binary we started from, so an out-of-band
		// replacement (make install, a package manager, another terminal) can be
		// noticed and reported as "restart" rather than as an available update.
		selfStamp: selfBinaryStamp(),
	}
	// One-time, gentle nudge for Apple Terminal users (no agent tabs, no OSC 52
	// clipboard). macTermNudge persists a marker the first time it fires, so this
	// surfaces once and then stays quiet.
	if note := macTermNudge(); note != "" {
		m.lastNotice = note
	}
	return m
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
const settingsTopLen = 7 // editor · group-by-repo · connection target · team · check-for-updates · update · github
// NB: voice dictation was row 6; it is roadmapped, not wired — see docs/roadmap.md.

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
			case 3: // Team & invites → the owner-only Team overlay (same as `I`)
				m.settings = nil
				return m.openTeamView()
			case 4: // Check for updates (re-poll GitHub) — stays open; line refreshes
				m.lastNotice = "checking for updates…"
				return m, tea.Batch(fetchLatestReleaseCmd(), checkSelfBinaryCmd(m.selfStamp), m.fetchOverviewCmd())
			case 5: // Update — same flow as 'u'/'U': client first, then the daemon (the
				// daemon update goes through the fleet-wide-restart warning + gate).
				m.settings = nil
				m.updateError = ""
				if m.clientUpdate {
					m.confirm = &confirmPrompt{verb: "update-client"}
				} else if m.daemonUpdate {
					m.confirm = &confirmPrompt{verb: "update-daemon"}
				} else {
					m.lastNotice = "already up to date"
				}
				return m, nil
			case 6: // GitHub → the self-serve identity pane
				m.settings = nil
				return m.openGithubView()
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

// toggleOwnerLens flips the ownership view between just-yours and all islands,
// re-anchoring the cursor on the island it was on (rows change) and clamping into
// the new, possibly-shorter list.
func (m tuiModel) toggleOwnerLens() tuiModel {
	anchor := m.selectedName()
	if m.ownerLens == lensAll {
		m.ownerLens = lensOwn
	} else {
		m.ownerLens = lensAll
	}
	m.selected = 0
	if anchor != "" {
		for i, row := range m.visibleRows() {
			if row.kind == rowIsland && row.island == anchor {
				m.selected = i
				break
			}
		}
	}
	if n := len(m.visibleRows()); m.selected >= n {
		m.selected = n - 1
	}
	if m.selected < 0 {
		m.selected = 0
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
	name   string
	verb   string
	err    error
	notice string // optional success notice to surface (e.g. an auto-renamed label)
}

// renameNotice returns an operator notice when the daemon auto-incremented a
// requested agent label that collided ("build" taken → "build-2"), or "" when
// the label landed as typed. Case-insensitive (a pure-casing match isn't
// flagged) and an empty requested label is never deduped — matching the
// daemon's UniqueAgentLabel rules. The response label is the source of truth.
func renameNotice(requested, final string) string {
	if strings.TrimSpace(requested) == "" || strings.EqualFold(requested, final) {
		return ""
	}
	return fmt.Sprintf("'%s' was taken — named it %s", requested, final)
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
	if m.demo {
		tick := m.demoTick
		return func() tea.Msg { return listMsg(demoIslands(tick)) }
	}
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
	if m.demo {
		tick := m.demoTick
		return func() tea.Msg { return overviewMsg(demoOverview(tick)) }
	}
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
	if !m.hostTerminalsAvailable() {
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
	if m.demo {
		tick := m.demoTick
		return func() tea.Msg {
			if info, ok := demoIsland(name, tick); ok {
				return detailMsg{info: info}
			}
			return nil
		}
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
	if m.demo {
		// No daemon, no setup-readiness or update checks — just the synthetic
		// fleet, polled on the tick so it animates. Keeps recordings clean.
		return tea.Batch(tea.SetWindowTitle("dejima"), m.fetchListCmd(), m.fetchOverviewCmd(), m.fetchPendingActionsCmd(), tickCmd())
	}
	return tea.Batch(tea.SetWindowTitle("dejima"), m.fetchListCmd(), m.fetchOverviewCmd(), m.fetchSetupReadinessCmd(), fetchLatestReleaseCmd(), tickCmd(), releaseTickCmd())
}

// latestReleaseMsg carries the newest published release tag, or the reason the
// check failed. Both matter: an empty tag with no reason is indistinguishable
// from "you're on the latest", which is how a rate-limited check came to report
// "already up to date" while silently knowing nothing.
type latestReleaseMsg struct {
	latest string
	err    error
}

// fetchLatestReleaseCmd queries GitHub for the latest release tag. Run sparingly
// (Init, manual refresh, and the slow releaseCheckInterval re-poll), never on the
// 2s tick — the GitHub API rate-limits unauthenticated callers.
func fetchLatestReleaseCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		v, err := selfupdate.LatestRelease(ctx)
		if err != nil {
			return latestReleaseMsg{err: err}
		}
		return latestReleaseMsg{latest: v}
	}
}

// selfBinaryStamp identifies the on-disk copy of the running executable, so a
// replacement underneath us is detectable. Empty when it can't be read.
func selfBinaryStamp() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// selfBinaryChangedMsg reports that the executable on disk is no longer the one
// this process was started from.
type selfBinaryChangedMsg struct{}

// checkSelfBinaryCmd compares the on-disk executable against the stamp taken at
// startup. A running process keeps its compiled-in version forever, so after an
// out-of-band update (make install, a package manager, another terminal) the TUI
// would go on reporting the OLD version and offering to "update" a client that
// is already current — and re-download it. Noticing the swap lets it say
// "restart" instead, which is the only thing that actually helps.
func checkSelfBinaryCmd(startStamp string) tea.Cmd {
	return func() tea.Msg {
		if startStamp == "" {
			return nil
		}
		if now := selfBinaryStamp(); now != "" && now != startStamp {
			return selfBinaryChangedMsg{}
		}
		return nil
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
		m.ticks++
		if m.demo {
			m.demoTick++ // advance the synthetic fleet so agent states churn on screen
		}
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
		return m, tea.Batch(releaseTickCmd(), fetchLatestReleaseCmd(), checkSelfBinaryCmd(m.selfStamp))

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
		if canOpenNewWindow() {
			// Open the new host shell in its own window/tab and keep the dashboard
			// up; refresh the band so the new terminal shows in the list.
			if err := m.openHostTermWindow(msg.id, ""); err != nil {
				m.lastError = err.Error()
			}
			return m, m.fetchTerminalsCmd()
		}
		m.connectTerminal = msg.id // no new-window backend: attach to the freshly created terminal in place
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
		m.tipTick++ // rotate the header Tip line on the poll cadence
		if msg != nil {
			// The caller's own identity (multi-tenant "who am I") drives the
			// ownership lens: callerOwner is what the your-islands view filters to,
			// callerRole gates the own/all toggle to the host owner.
			m.callerOwner, m.callerRole = msg.Owner, msg.Role
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

	case selfBinaryChangedMsg:
		// The binary was replaced out of band; this process is now the stale one.
		// Offering another update would re-download what is already installed.
		m.clientUpdate = false
		m.restartPending = "a newer dejima is installed on disk — restart dejima to apply it"
		return m, nil

	case islandSecretsMsg:
		if v := m.secretsPane; v != nil && v.island == msg.island {
			v.loading = false
			if msg.err != nil {
				v.err = msg.err.Error()
			} else {
				v.err, v.secrets = "", msg.secrets
				if v.cursor >= len(v.secrets) { // a removal can shrink the list
					v.cursor = max(0, len(v.secrets)-1)
				}
				if msg.added != "" {
					v.restartPending = true
					v.notice = "stored " + msg.added
				}
			}
		}
		return m, nil

	case latestReleaseMsg:
		if msg.latest != "" {
			m.latestRelease = msg.latest
			m.updateCheckErr = ""
			m.clientUpdate = selfupdate.Evaluate(version.Version, msg.latest, selfupdate.DetectMode()).UpdateAvailable
			m.daemonUpdate = daemonUpdateAvailable(msg.latest, m.overview)
		} else if msg.err != nil {
			// Remember WHY we don't know, so [U] can say "couldn't check"
			// instead of claiming the build is current.
			m.updateCheckErr = msg.err.Error()
		}
		return m, nil

	case clientUpdatedMsg:
		m.updating = "" // the in-progress banner gives way to the result
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
		m.updating = "" // the in-progress banner gives way to the result
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

	case tokensLoadedMsg:
		if m.team != nil {
			m.team.applyLoaded(msg)
		}
		return m, nil

	case aggregateLoadedMsg:
		if m.aggregate != nil {
			m.aggregate.applyLoaded(msg)
		}
		return m, nil

	case tokenMintedMsg:
		if m.team != nil {
			m.team.minting = false
			if msg.err != nil {
				m.team.actionErr = msg.err.Error()
				// An encode failure still mints the token — reload so the orphaned
				// token shows in the list (and can be revoked) right away.
				if msg.resp != nil {
					m.team.loading = true
					return m, m.loadTokensCmd()
				}
			} else {
				m.team.minted = msg.resp
				m.team.mintedBlob = msg.blob
			}
		}
		return m, nil

	case clipboardCopiedMsg:
		// The OSC-52 escape was already written; surface the ✓ confirmation.
		m.lastNotice = msg.notice
		return m, nil

	case githubIdentitiesMsg:
		if m.github != nil {
			m.github.loading = false
			if msg.err != nil {
				m.github.err = msg.err.Error()
			} else {
				m.github.identities = msg.identities
				m.github.err = ""
			}
		}
		return m, nil

	case tokenRevokedMsg:
		if m.team != nil {
			if msg.err != nil {
				m.team.actionErr = "revoke: " + msg.err.Error()
				return m, nil
			}
			// Reload so the list reflects the revoke immediately.
			m.team.loading = true
			return m, m.loadTokensCmd()
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

	case spawnGrantLoadedMsg:
		if m.spawnGrant != nil && m.spawnGrant.island == msg.island {
			m.spawnGrant.applyLoaded(msg)
		}
		return m, nil

	case spawnGrantMutatedMsg:
		if m.spawnGrant != nil && m.spawnGrant.island == msg.island {
			m.spawnGrant.busy = false
			if msg.err != nil {
				verb := "grant"
				if msg.revoked {
					verb = "revoke"
				}
				m.spawnGrant.actionErr = verb + ": " + msg.err.Error()
				return m, nil
			}
			// Reload so the overlay reflects the new granted/used state immediately.
			m.spawnGrant.loading = true
			m.lastNotice = "sub-agent budget updated"
			if msg.revoked {
				m.lastNotice = "sub-agent budget revoked"
			}
			return m, m.loadSpawnGrantCmd(msg.island)
		}
		return m, nil

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
			// Surface a fresh Claude credential auto-seed once (a3 wanted a surprise
			// capture VISIBLE, not silent). It also renders in the island's Recent
			// feed below; this is the prominent one-time banner.
			if note, at, ok := claudeAutoSeedNotice(msg.events, m.seededNoticeAt, time.Now()); ok {
				m.lastNotice = note
				m.seededNoticeAt = at
			}
		}
		return m, nil

	case errMsg:
		if m.demo {
			return m, nil // demo never talks to a daemon; ignore stray fetch errors
		}
		if msg.err != nil {
			m.lastError = msg.err.Error()
			// A daemon that's gone unreachable: attach an actionable diagnosis
			// (computed here, not in the renderer, since the local path shells out).
			// Local → why dejimad on this machine is down + how to fix; remote → calm
			// recovery steps for a teammate/laptop pointed at a server (safe work,
			// auto-retry, tailnet check, reinstall, ask-operator).
			if isConnectionError(msg.err) {
				if m.activeHost == "" {
					d := diagnoseLocalDaemon()
					m.daemonHelp = &d
				} else {
					d := diagnoseRemoteDaemon(m.activeHost)
					m.daemonHelp = &d
				}
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
		} else if msg.notice != "" {
			m.lastNotice = msg.notice // e.g. "'build' was taken — named it build-2"
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
		if msg.notice != "" {
			m.lastNotice = msg.notice // e.g. "'build' was taken — named it build-2"
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
				if isGitHubIdentityGateError(msg.err) {
					// Upgrade the daemon's bare "needs a GitHub identity" refusal into
					// a guided connect step instead of a dead-end error (surface 2).
					m.creator.gateRepo = m.creator.resolution.Repo
					m.creator.step = stepGitHubGate
					m.creator.err = ""
				} else {
					m.creator.err = msg.err.Error()
				}
				return m, nil
			}
			m.creator = nil
			// Open the new island in a new tab so the dashboard stays up; fall back
			// to attaching in this terminal when there's no new-window backend.
			// Attach straight into the PRIMARY agent (by id) rather than the bare
			// island, so a multi-agent island doesn't dump a non-technical operator
			// onto the `connect` "Attach which? [1]" picker right after create.
			if canOpenNewWindow() {
				if err := m.openInNewWindow(msg.name, msg.agentID, msg.agentLabel); err != nil {
					m.lastError = err.Error()
				} else {
					m.lastNotice = "created " + msg.name + " — opened in a new tab"
				}
				return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
			}
			m.connectTo, m.connectAgent = msg.name, msg.agentID // drop straight into the primary agent
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
	// A confirmation modal is top-most: it owns keys whenever open, even over an
	// overlay that spawned it (e.g. approve / deny / approve-rule from the
	// approvals overlay). Otherwise that overlay's key handler swallows the typing
	// and the Enter, leaving the modal unusable.
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
	// The Secrets pane owns keys while open.
	if m.secretsPane != nil {
		return m.secretsKey(msg)
	}
	// The help overlay owns keys while shown.
	if m.help {
		switch msg.String() {
		case "?", "esc", "q":
			m.help = false
		case "a":
			m.helpMore = !m.helpMore // expand / collapse the reference dropdown
			m = m.scrollHelpLines(0) // re-clamp scroll to the new content height
		case "j", "down":
			m = m.scrollHelpLines(1)
		case "k", "up":
			m = m.scrollHelpLines(-1)
		case "pgdown", "ctrl+d", " ":
			m = m.scrollHelpLines(m.helpInnerHeight() - 1)
		case "pgup", "ctrl+u":
			m = m.scrollHelpLines(-(m.helpInnerHeight() - 1))
		case "g", "home":
			m.helpScroll = 0
		case "G", "end":
			m = m.scrollHelpLines(1 << 30)
		}
		return m, nil
	}
	// The per-row action menu owns keys while open.
	if m.menu != nil {
		return m.actionMenuKey(msg)
	}
	// The GitHub identity pane owns keys while open.
	if m.github != nil {
		return m.githubKey(msg)
	}
	// The settings overlay owns keys while open.
	if m.settings != nil {
		return m.settingsKey(msg)
	}
	// The per-island resources overlay owns keys while open.
	if m.resEditor != nil {
		return m.resEditorKey(msg)
	}
	// The per-island sub-agent-budget overlay owns keys while open.
	if m.spawnGrant != nil {
		return m.spawnGrantKey(msg)
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
	// The visual-identity editor owns keys while open.
	if m.identity != nil {
		return m.identityKey(msg)
	}
	// The Team / invite overlay owns keys while open.
	if m.team != nil {
		return m.teamKey(msg)
	}
	// The host-utilization panel owns keys while open.
	if m.aggregate != nil {
		return m.aggregateKey(msg)
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
		m.helpScroll = 0 // always open at the top (the title)
		return m, nil
	case "A":
		// Audit-ledger viewer (chain-verification + recent governance activity).
		return m.openAuditView()
	case "T":
		// Trust surface — what the highlighted island can reach (Port · MCP ·
		// links · capabilities). Agent rows inherit their island's grants.
		return m.openGrantsView(m.selectedName())
	case "$":
		// Per-island secrets. '$' for the shell-variable association — these are
		// environment variables to the tools that use them.
		return m.openSecretsView(m.selectedIslandName())
	case "V":
		// Action-gate approvals — the queue of cross-island actions awaiting a
		// decision (reView) + the active auto-approve rules. Refresh both on open.
		// (G is jump-to-bottom.)
		m.approvals = &approvalsView{}
		return m, tea.Batch(m.fetchPendingActionsCmd(), m.fetchPolicyCmd())
	case "I":
		// Team / Invite — owner-only: mint a teammate's token (role + scope), show
		// the copyable invite, list + revoke issued tokens. The list call gates the
		// view to owners (a non-owner caller 403s → an explanatory panel).
		return m.openTeamView()
	case "#":
		// Toggle bare agent ids on/off live (names-only by default). The same
		// reveal the --ids flag / DEJIMA_SHOW_IDS set at launch; every agent render
		// resolves through agentDisplay, so flipping showIDs re-labels on next draw.
		showIDs = !showIDs
		if showIDs {
			m.lastNotice = "showing agent ids (name (id)) — press # to hide"
		} else {
			m.lastNotice = "names only — press # to reveal ids"
		}
		return m, nil
	case "!":
		// Demo-only: stage/unstage the action-gate scene (pending actions + badge)
		// so the hero fleet shot stays clean until you want the approval clip.
		if m.demo {
			m.demoApprovals = !m.demoApprovals
			return m, m.fetchPendingActionsCmd()
		}
	case "P":
		// Port scope-picker for the selected island (brokered host-file grants).
		// Capital P; lowercase p is group-by-repo.
		if name := m.selectedName(); name != "" {
			return m.openScopeView(name)
		}
		return m, nil
	case "n":
		return m.openCreator()
	case "/", "`":
		// Toggle + focus the pinned host-terminal band (above the island list).
		// `/` is the primary key; backtick kept as an alias.
		if m.hostTerminalsAvailable() {
			m.bandExpanded = true
			m.bandFocused = true
			if m.bandSel >= m.bandRowCount() {
				m.bandSel = 0
			}
		} else {
			// Say why the key's a no-op rather than leaving the operator guessing.
			// (On by default now; this only shows when the daemon disabled it.)
			m.lastNotice = hostTerminalsOffNote
		}
		return m, nil
	case "t":
		// New host terminal (uncontained shell on the daemon host) + attach.
		if m.hostTerminalsAvailable() {
			return m, m.createTerminalCmd("")
		}
		m.lastNotice = hostTerminalsOffNote
		return m, nil
	case "s", "S", ",":
		// General settings (editor · group-by-repo · connection target). Both s and
		// S open it (S used to be SSH setup — that moved into the Server menu [H] and
		// the per-row actions menu [m]). Server switching lives inside here rather
		// than owning its own hotkey.
		return m.openSettings(), nil
	case ">":
		// In-island /workspace shell for the selected island. (Enter opens the
		// island's agents; `>` — a shell prompt — opens the contained shell.)
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
	case "%":
		// Host-utilization panel — the privacy-preserving aggregate (counts +
		// total mem/cpu/disk, no names). Multi-tenant P4; readable by any caller.
		return m.openAggregateView()
	case "O":
		// Ownership lens (multi-tenant): flip the host-owner's view between your
		// islands (default) and every island on the daemon. Owner-only — a teammate's
		// list is already owner-filtered server-side (P2), so there's nothing to
		// toggle; say so rather than silently no-op. Also inert on a daemon that
		// predates the ownership model (no caller identity reported).
		switch {
		case m.callerOwner == "":
			m.lastNotice = "owner filtering activates once the daemon reports island ownership"
			return m, nil
		case m.callerRole != "owner":
			m.lastNotice = "you already see only your own islands"
			return m, nil
		}
		mm := m.toggleOwnerLens()
		if mm.ownerLens == lensAll {
			mm.lastNotice = "showing ALL islands on this daemon"
		} else {
			mm.lastNotice = "showing your islands only"
		}
		return mm, nil
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
	case "u", "U":
		// Update Dejima: the client first (this local binary), then the daemon if the
		// client is already current and the daemon is behind. The daemon update goes
		// through the explicit "this RESTARTS the daemon and closes all terminals
		// fleet-wide" confirm + the defer-while-attached gate (see the update-daemon
		// verb) — so it's a consented action, not a silent side effect, and clients
		// reconnect through the restart. Also reachable deliberately via [H]. (Old
		// lowercase-u = island-upgrade moved into the [m] actions menu.)
		m.updateError = "" // clear a prior failure when retrying
		if m.clientUpdate {
			m.confirm = &confirmPrompt{verb: "update-client"}
		} else if m.daemonUpdate {
			m.confirm = &confirmPrompt{verb: "update-daemon"}
		} else if m.updateCheckErr != "" {
			// We never learned what the latest release is, so "up to date" would
			// be a claim we can't support.
			m.updateError = "couldn't check for updates: " + m.updateCheckErr
		} else {
			m.lastNotice = "already up to date"
		}
		return m, nil
	case "b":
		if !m.building {
			m.confirm = &confirmPrompt{verb: "build-image"}
		}
	case "d":
		if name := m.selectedName(); name != "" {
			m.confirm = &confirmPrompt{verb: "purge", island: name}
		}
	case "H":
		// Server menu — host/daemon controls (update daemon, SSH setup, build image,
		// refresh). A discoverable surface mirroring the per-row [m] actions menu; the
		// direct keys (/, b, R) still work. The daemon self-update lives ONLY here,
		// behind an explicit fleet-wide-restart warning.
		return m.openServerMenu(), nil
	case "esc":
		// Dismiss whichever sticky update banner is showing (no overlay here):
		// a failure, or an applied-but-needs-restart notice. (Green fades itself.)
		m.updateError = ""
		m.restartPending = ""
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
	case "remove-secret":
		// Typing the NAME, like purge types the island name — removing a secret
		// breaks whatever tool was using it, so it shouldn't ride on one key.
		if strings.TrimSpace(c.answer) == c.agent {
			if v := m.secretsPane; v != nil {
				v.restartPending = true
			}
			return m, m.removeSecretCmd(c.island, c.agent)
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
			m.updating = "downloading the client update…"
			m.updateError = ""
			return m, applyClientUpdateCmd(m.latestRelease)
		}
	case "update-daemon":
		if strings.ToLower(strings.TrimSpace(c.answer)) == "y" {
			// A source-install daemon rebuilds (git pull + make install) before it
			// restarts — tens of seconds. Show progress so it doesn't look frozen.
			m.updating = "updating the daemon (building + installing, then restarting)…"
			m.updateError = ""
			return m, m.updateDaemonCmd(c.force)
		}
	}
	// Nothing above acted, which means the typed answer didn't satisfy the gate.
	// Say so. A confirmation that silently closes is indistinguishable from one
	// that worked — which is exactly how "delete does nothing, no error" went
	// unexplained: the operation was never attempted and nothing said why.
	if want := confirmExpectation(c); want != "" {
		typed := strings.TrimSpace(c.answer)
		if typed == "" {
			m.lastError = fmt.Sprintf("%s cancelled — %s", c.verb, want)
		} else {
			m.lastError = fmt.Sprintf("%s not confirmed — %s (you typed %q)", c.verb, want, typed)
		}
	}
	return m, nil
}

// confirmExpectation describes what a typed confirmation needed, for the
// message shown when it didn't match. Mirrors the gates in runConfirmed —
// a verb missing here degrades to silence, so keep the two together.
func confirmExpectation(c confirmPrompt) string {
	switch c.verb {
	case "purge":
		return fmt.Sprintf("type the island name %q exactly", c.island)
	case "remove-agent":
		return fmt.Sprintf("type the agent id %q exactly", c.agent)
	case "remove-secret":
		return fmt.Sprintf("type the secret name %q exactly", c.agent)
	case "reset", "upgrade", "recreate-island", "build-image", "force-purge",
		"remove-terminal", "approve-action", "open-all-agents", "setup-ssh",
		"update-client", "update-daemon":
		return `type "y" to confirm`
	}
	// relabel-agent / rename-island / deny-action always act; approve-rule
	// depends on the action still being pending, not on the typed text.
	return ""
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
		ag, err := m.client.RelabelAgent(ctx, name, agentID, label)
		notice := ""
		if err == nil && ag != nil {
			notice = renameNotice(label, ag.Label) // daemon auto-increments collisions
		}
		return opCompleteMsg{name: name, verb: "relabel-agent", err: err, notice: notice}
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
	rowSecrets                    // the island's secrets line, below its agents
	rowNewIsland                  // the trailing "+ new island" affordance
	rowTerminal                   // a host terminal (uncontained shell) in the Host section
	rowNewTerminal                // the "+ new terminal" affordance
)

// treeRow is one visible line in the list.
type treeRow struct {
	kind    rowKind
	island  string
	agentID string
	// depth nests agent-spawned sub-agents under their spawner: 0 = a top-level
	// agent, 1+ = a sub-agent indented beneath its parent. Only meaningful for
	// rowAgent.
	depth int
}

// hostTerminalsEnabled reports whether the daemon offers host terminals.
// Driven by the overview capability. Prefer hostTerminalsAvailable at UI/poll
// sites — this bare flag ignores the caller's role.
func (m tuiModel) hostTerminalsEnabled() bool {
	return m.overview != nil && m.overview.HostTerminalsEnabled
}

// hostTerminalsAvailable reports whether host terminals are usable by THIS
// caller: the daemon offers them AND the caller isn't a non-owner. Host
// terminals are uncontained host shells — an owner-only surface (GET/POST
// /v1/terminals is capOwner in roleauth) — so an operator/viewer must not poll,
// render, or try to open them: each call 403s "requires owner", and a passive
// poll flashes an error the teammate can't act on. We exclude the known
// non-owner roles (rather than requiring role=="owner") so an owner is never
// hidden if the daemon didn't stamp a role. The caller's role rides in on the
// same overview response that sets HostTerminalsEnabled, so it's known whenever
// this can be true.
func (m tuiModel) hostTerminalsAvailable() bool {
	return m.hostTerminalsEnabled() && m.callerRole != "operator" && m.callerRole != "viewer"
}

// hostTerminalsOffNote explains the `/` and `t` no-op when the daemon has host
// terminals disabled. On by default, so this is the explicit-opt-out case. Kept
// short so it isn't truncated in the footer's status strip.
const hostTerminalsOffNote = "host terminals are disabled on this daemon (--host-terminals=false)"

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
			ordered, depth := orderedAgents(isl.Agents)
			for i, a := range ordered {
				rows = append(rows, treeRow{kind: rowAgent, island: isl.Name, agentID: a.ID, depth: depth[i]})
			}
			rows = append(rows, treeRow{kind: rowAddAgent, island: isl.Name})
			// Secrets sit BELOW the agents: they're island-scoped configuration,
			// not another thing that runs. Always present, so an operator learns
			// the feature exists without having to go looking for a keybinding.
			rows = append(rows, treeRow{kind: rowSecrets, island: isl.Name})
		}
	}
	rows = append(rows, treeRow{kind: rowNewIsland})
	// Host terminals (operator shells on the daemon host) used to live at the
	// tail of this list; they now have their own pinned band above it. See
	// renderBand / bandKey.
	return rows
}

// orderedAgents reorders an island's agents so each agent-spawned sub-agent sits
// immediately after its spawner, and returns a parallel depth slice (0 =
// top-level, 1+ = nested under a spawner via AgentInfo.SpawnedBy). Order within
// each group is preserved. An agent whose SpawnedBy isn't present in the island
// (or empty) is top-level; any agent a cycle would otherwise hide is swept in at
// the end, so the list never silently drops a row.
func orderedAgents(agents []api.AgentInfo) (ordered []api.AgentInfo, depth []int) {
	present := make(map[string]bool, len(agents))
	for _, a := range agents {
		present[a.ID] = true
	}
	children := map[string][]api.AgentInfo{}
	for _, a := range agents {
		parent := a.SpawnedBy
		if parent == "" || !present[parent] {
			parent = "" // top-level (orphaned lineage shows at the root, not hidden)
		}
		children[parent] = append(children[parent], a)
	}
	seen := make(map[string]bool, len(agents))
	var walk func(parentID string, d int)
	walk = func(parentID string, d int) {
		for _, a := range children[parentID] {
			if seen[a.ID] {
				continue // cycle guard
			}
			seen[a.ID] = true
			ordered = append(ordered, a)
			depth = append(depth, d)
			walk(a.ID, d+1)
		}
	}
	walk("", 0)
	// Sweep any agent a cycle left unvisited so nothing disappears from the list.
	for _, a := range agents {
		if !seen[a.ID] {
			ordered = append(ordered, a)
			depth = append(depth, 0)
		}
	}
	return ordered, depth
}

const (
	lensOwn = iota // just the caller's own islands (the default)
	lensAll        // every island the caller can see (the host-owner's all-view)
)

// ownedIslands applies the ownership lens: in lensOwn, only the caller's own
// islands; in lensAll (or before the daemon reports the caller's owner id — see
// callerOwner), the full list unchanged. Fail-open on an empty callerOwner so the
// lens never hides everything on a daemon that predates the ownership model — it
// only ever narrows once we actually know who "you" are.
func (m tuiModel) ownedIslands() []api.IslandInfo {
	if m.ownerLens == lensAll || m.callerOwner == "" {
		return m.islands
	}
	out := make([]api.IslandInfo, 0, len(m.islands))
	for _, isl := range m.islands {
		if isl.Owner == m.callerOwner {
			out = append(out, isl)
		}
	}
	return out
}

// orderedIslands returns the islands in display order: the ownership-lensed set
// as-is, or — when grouped — reordered so islands sharing a repo are contiguous
// (first-seen repo order, original order within each repo). Drives both the row
// list and the rendered group headers, so navigation indices and headers stay
// consistent.
func (m tuiModel) orderedIslands() []api.IslandInfo {
	islands := m.ownedIslands()
	if !m.grouped {
		return islands
	}
	idx := map[string]int{}
	var groups [][]api.IslandInfo
	for _, isl := range islands {
		i, ok := idx[isl.Repo]
		if !ok {
			i = len(groups)
			idx[isl.Repo] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], isl)
	}
	out := make([]api.IslandInfo, 0, len(islands))
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
		items = append(items, actionMenuItem{label: "Sub-agent budget… (spawn grant)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openSpawnGrantEditor(islandName)
		}})
		items = append(items, actionMenuItem{label: "Secrets… (tokens this island's tools use)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openSecretsView(islandName)
		}})
		items = append(items, actionMenuItem{label: "Port scopes… (brokered host-file access)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openScopeView(islandName)
		}})
		items = append(items, actionMenuItem{label: "Color & glyph… (visual identity)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
			return mm.openIdentityEditor(islandName)
		}})
		if m.overview != nil && m.overview.SSHAddr != "" {
			items = append(items, actionMenuItem{label: "SSH setup (this device → every island)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
				return mm.startSSHSetup()
			}})
		}
		if isl.Container == "running" {
			upgradeName := isl.Name
			items = append(items,
				actionMenuItem{label: "Reset agent state", key: "r", danger: true},
				actionMenuItem{label: "Upgrade to the current image", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
					mm.confirm = &confirmPrompt{verb: "upgrade", island: upgradeName}
					return mm, nil
				}},
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
	if it.disabled {
		return m, nil // context-only line (e.g. "daemon up to date") — nothing to do
	}
	global := m.menu.global
	target := m.menu.row
	m.menu = nil
	// A global menu (the Server menu [H]) isn't anchored to a list row, so there's
	// nothing to re-anchor — its items act on the host/daemon.
	if !global {
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
	}
	if it.open != nil {
		return it.open(m)
	}
	return m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(it.key)})
}

// startSSHSetup arms the account-wide SSH-setup confirm (authorize this machine's
// key fleet-wide + write ~/.ssh/config for every island), or explains the façade
// is off. Reachable from the Server menu [H] and the per-row actions menu [m];
// it used to be the top-level S key (now Settings).
func (m tuiModel) startSSHSetup() (tea.Model, tea.Cmd) {
	if m.overview == nil || m.overview.SSHAddr == "" {
		m.lastError = "ssh façade is off — start dejimad with --ssh (e.g. `dejima service install --ssh :2222`)"
		return m, nil
	}
	m.confirm = &confirmPrompt{verb: "setup-ssh"}
	return m, nil
}

// openServerMenu builds the Server menu [H] — host/daemon controls that used to be
// scattered across top-level keys. It mirrors the per-row actions menu ([m]) but
// is global (not anchored to a list row). The daemon self-update lives ONLY here,
// behind an explicit fleet-wide-restart warning, so it can never fire as a side
// effect of a routine keypress. The direct keys (/, b, R) still work; this is an
// additional, discoverable surface.
func (m tuiModel) openServerMenu() tuiModel {
	var items []actionMenuItem

	// Update daemon — the fleet-wide-restart action. Only actionable when the
	// daemon is actually behind; otherwise a greyed context line.
	if m.daemonUpdate {
		items = append(items, actionMenuItem{
			label: "Update daemon (RESTARTS it — closes all terminals fleet-wide)",
			open: func(mm tuiModel) (tea.Model, tea.Cmd) {
				mm.updateError = "" // clear a prior failure when retrying
				mm.confirm = &confirmPrompt{verb: "update-daemon"}
				return mm, nil
			},
		})
	} else {
		items = append(items, actionMenuItem{label: "Update daemon — up to date", disabled: true})
	}

	items = append(items, actionMenuItem{label: "Set up SSH (this device → every island)", open: func(mm tuiModel) (tea.Model, tea.Cmd) {
		return mm.startSSHSetup()
	}})
	items = append(items, actionMenuItem{label: "Build island image", key: "b"})
	if m.hostTerminalsAvailable() {
		items = append(items, actionMenuItem{label: "Host terminals", key: "/"})
	}
	items = append(items, actionMenuItem{label: "Refresh", key: "R"})

	m.menu = &actionMenu{title: "Server · host & daemon", items: items, global: true}
	return m
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
	case rowSecrets:
		return m.openSecretsView(row.island)
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
// selectedIslandName returns the island the cursor is on — the island itself,
// or the island owning the selected agent row. "" when nothing is selected.
func (m tuiModel) selectedIslandName() string {
	if r := m.currentRow(); r.island != "" {
		return r.island
	}
	if len(m.islands) == 1 {
		return m.islands[0].Name // unambiguous with a single island
	}
	return ""
}

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
	// styleSubAgent renders agent-spawned sub-agent rows: dimmer than styleMuted
	// and italic, so a transient sub-agent reads as subordinate to its spawner.
	styleSubAgent = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7a90")).Italic(true)
	styleFooter   = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8"))
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
		return styleAccent.Render("loading…")
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
		// The help lists every key across four sections — taller than the screen,
		// so window it (scrollWindow adds a "↕ a–b of n" hint) and let PgUp/PgDn/jk
		// scroll. Opening always resets to the top, so the title is visible.
		content, _ := scrollWindow(m.renderHelp(), m.helpInnerHeight(), m.helpScroll)
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(content)
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
	if m.spawnGrant != nil {
		box := styleMenuBox.Render(m.renderSpawnGrantEditor())
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
	if m.identity != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderIdentityView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.team != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderTeamView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.github != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.renderGithubView())
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.secretsPane != nil {
		body := stylePane.Width(m.width - 2).Height(m.height - hh - 2).Render(m.secretsPane.view(m.width - 4))
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	if m.aggregate != nil {
		box := styleMenuBox.Render(m.renderAggregateView())
		body := lipgloss.Place(m.width-2, m.height-hh-2, lipgloss.Center, lipgloss.Center, box)
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
		return fmt.Sprintf(" ! %d cross-island action(s) %s   ·   [V] review", n, tail),
			fmt.Sprintf(" ! %d to approve ", n), st, true
	case m.updating != "":
		// An update is being applied right now. A source daemon update rebuilds for
		// tens of seconds before restarting; without this in-progress banner the TUI
		// looks frozen between the keypress and the result. Blue; cleared by the
		// result (applied / restart-pending / error).
		return " ⟳ " + m.updating,
			" ⟳ updating… ", styleWarnBroadcast, true
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
	case m.clientUpdate, m.daemonUpdate:
		// [U] applies the update — the client first, then the daemon if it's behind.
		// updateParts() names whichever is stale (client / daemon / both). A daemon
		// update goes through the fleet-wide-restart warning + attach-gate; also
		// reachable via the Server menu [H].
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

	// server: <label>  ·  [s] switch  [·  ssh <addr>]
	// [s] opens settings, where "connection target" changes which server the
	// dashboard attaches to. The ssh hint appears only when the daemon has the
	// SSH-façade listener on (--ssh); `dejima ssh config <island> --install`
	// resolves the full address.
	serverLine := styleMuted.Render("server: ") + styleAccent.Render(label)
	if m.activeSource == "env" {
		serverLine += styleMuted.Render(" via $DEJIMA_HOST")
	}
	serverLine += styleMuted.Render("  ·  ") + styleAccent.Render("[s]") + styleMuted.Render(" switch")
	// Team controls are owner-only; surface the hint unless we know the caller is
	// a teammate (fail-open before the daemon reports identity, matching the lens).
	if m.callerRole == "" || m.callerRole == "owner" {
		serverLine += styleMuted.Render("  ·  ") + styleAccent.Render("[I]") + styleMuted.Render(" team")
	}
	if m.overview != nil && m.overview.SSHAddr != "" {
		serverLine += styleMuted.Render("  ·  ssh ") + styleAccent.Render(m.overview.SSHAddr)
	}

	infoW := m.width - lipgloss.Width(logoArt[0]) - 9

	// The top line is the announcement bar: normally blank (keeping the 7-row
	// info block aligned with the logo), but a full-width highlighted broadcast
	// when there's something to say (an available update, today).
	topLine := ""
	if full, _, style, ok := m.announcement(); ok {
		topLine = style.Width(infoW).Render(full)
	}

	// The two former subtitle lines are collapsed into one, freeing a row for a
	// rotating Tip (feature discovery) under the tagline. The Tip is one short
	// line; the full set lives in `?` help.
	tipLine := styleAccent.Render("Tip") + styleMuted.Render("  "+currentTip(m.tipTick))
	info := strings.Join([]string{
		topLine,
		styleTitle.Render("Dejima") + styleMuted.Render(" — isolated islands for AI coding agents, on your own hardware"),
		"",
		// The static "each island is a repo in its own container" line explained
		// the product to someone already looking at it, every single frame. The
		// rotating tip earns that row instead.
		//
		// Tip sits directly ON the key legend deliberately: both answer "what can
		// I do here", so they read as one block. The spare row goes below them
		// instead, separating that guidance from the server line, which is status
		// rather than something to act on. Floating the tip between two blanks
		// left it looking orphaned. Still exactly 7 rows, to match the logo.
		tipLine,
		styleAccent.Render("↑/↓") + styleMuted.Render(" pick  ·  ") + styleAccent.Render("⏎") + styleMuted.Render(" open agent(s)  ·  ") + styleAccent.Render(">") + styleMuted.Render(" shell  ·  ") + styleAccent.Render("s") + styleMuted.Render(" settings  ·  ") + styleAccent.Render("?") + styleMuted.Render(" help"),
		"",
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

// helpInnerHeight is the visible content height of the help overlay pane: the
// full-width body (m.height - header - 2) minus the pane's top+bottom border.
func (m tuiModel) helpInnerHeight() int {
	h := m.height - lipgloss.Height(m.renderHeader()) - 4
	if h < 3 {
		h = 3
	}
	return h
}

// scrollHelpLines moves the help overlay by delta lines, clamped to content
// (0..maxOff), so PgUp/PgDn/jk can never scroll past either end.
func (m tuiModel) scrollHelpLines(delta int) tuiModel {
	_, maxOff := scrollWindow(m.renderHelp(), m.helpInnerHeight(), 0)
	m.helpScroll += delta
	if m.helpScroll > maxOff {
		m.helpScroll = maxOff
	}
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
	return m
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
		// First-run: no islands yet. The "+ new island" row is already selected
		// (visibleRows always ends with it), so Enter creates one — but say so,
		// since a bare empty pane gave no hint that the TUI itself can set one up
		// (people were told to quit and use the CLI). Make it an obvious prompt.
		// Render it as a SELECTED row, not a heading. The row genuinely is
		// selected — Enter already worked — but with no ▶ and no highlight it
		// read as decoration, so people didn't know it was the thing to press.
		body := styleSelected.Render("▶ + Set up your first island") + "\n\n" +
			styleAccent.Render("Press ⏎ to start") + styleMuted.Render(" — you'll pick a source: a local repo, a git URL,\nor browse your GitHub repos. (`n` or `+` opens this anytime.)")
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
			// Agent rows above branch (├); the secrets row below now caps the group.
			line = "   " + styleMuted.Render("└ + add agent")
		case rowSecrets:
			// Caps the island's child group (└). Always shown, so the feature is
			// discoverable without knowing a keybinding — the count tells an
			// operator at a glance whether this island has any.
			isl := byName[row.island]
			label := glyphSecrets + " secrets"
			if n := isl.SecretsCount; n > 0 {
				label = fmt.Sprintf("%s secrets (%d)", glyphSecrets, n)
			}
			// Indented a level deeper than the agents: secrets are island
			// configuration that sits beneath the agent group, not a peer of it.
			line = "     " + styleMuted.Render("└ "+label)
		case rowAgent:
			isl := byName[row.island]
			a := agentByID(isl, row.agentID)
			if row.depth > 0 {
				// An agent-spawned sub-agent: indent under its spawner and render it
				// dim/italic with an ephemeral marker, so it reads as subordinate and
				// transient rather than a peer of the island's own agents.
				indent := "   " + strings.Repeat("  ", row.depth)
				line = indent + styleMuted.Render("└ ") + subAgentRowText(a)
			} else {
				line = "   " + styleMuted.Render("├ ") + agentRowText(a, labelIsAmbiguous(isl.Agents, a))
			}
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
			idStyle, idGlyph := islandVisual(isl)
			line = fmt.Sprintf("%s %s %s  %s  %s",
				caret, glyphFor(isl), idStyle.Render(idGlyph),
				idStyle.Render(fmt.Sprintf("%-14s", label)),
				shortStatus(isl, m.dirtyOps[isl.Name]))
			// In the all-islands lens, tag each row with its owner so the host owner
			// can tell whose island is whose. Omitted in the your-islands lens (they're
			// all yours) and when the daemon reports no owner.
			if m.ownerLens == lensAll && isl.Owner != "" {
				line += "  " + styleMuted.Render("@"+isl.Owner)
			}
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

	// glyphSecrets marks the per-island secrets row: U+26B7, a monochrome
	// boxless key symbol.
	//
	// NOT the padlock emoji (U+1F512). Emoji are East Asian WIDE — the terminal
	// draws two cells — but lipgloss/wcwidth count the text-presentation form as
	// one. That one-cell disagreement wraps the row, and Bubble Tea's diff
	// renderer (which counts newlines, not display width) then leaves the
	// wrapped remainder on screen: the whole view duplicates on every repaint.
	// The VARIATION SELECTOR-15 trick suppressed the colour but not the width,
	// so it kept the bug. A text-default symbol that measures as one cell
	// everywhere is the only safe kind here — like ◆/■/❯ above.
	glyphSecrets = "⚷"
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

// agentDisplayName is the human-facing name for an agent: its label by default,
// the id appended ("label (id)") only when --ids/DEJIMA_SHOW_IDS is on, and the
// bare id when there's no label. Delegates to the shared agentDisplay helper so
// the TUI honors the same names-only-unless-asked reveal toggle as the CLI (the
// `#` key flips it live; see handleKey). Every agent surface in the TUI resolves
// through here, so this is the single place that governs id visibility.
func agentDisplayName(a api.AgentInfo) string {
	return agentDisplay(a.Label, a.ID)
}

// agentDisplayIn resolves an agent id to its display name (label, else id)
// within an island the operator can see — for surfaces that carry a bare id
// (e.g. the action-gate queue's from_agent/to_agent). Falls back to the id when
// the island/agent isn't in the local roster. id stays the addressing handle;
// this is display only.
func (m tuiModel) agentDisplayIn(island, agentID string) string {
	if agentID == "" {
		return ""
	}
	if isl, ok := m.islandByName(island); ok {
		for _, a := range isl.Agents {
			if a.ID == agentID {
				return agentDisplayName(a)
			}
		}
	}
	return agentID
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
	if !m.hostTerminalsAvailable() {
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
			styleHeader.Render("▸ Host"), dot, styleMuted.Render(count),
			styleMuted.Render("· not contained"), styleMuted.Render("[/] expand"))
		return clip(line), 1
	}

	var b strings.Builder
	// Header carries the action hints inline (rather than a separate footer line)
	// so the pinned band stays compact: ⏎ attach · d delete · / close.
	b.WriteString(styleHeader.Render("▾ Host terminals") + " " +
		styleMuted.Render("· not contained") + "   " +
		styleMuted.Render("⏎ open · d delete · [/] collapse") + "\n")
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
// "+ new terminal" row, attach (⏎), create, delete (d/del), and collapse
// (/ · esc · backtick). `/` toggles the band both ways — the same key that
// opened it closes it, matching the "[/] collapse" hint. Reuses the same
// commands as the old inline Host rows, so terminal behavior is unchanged.
func (m tuiModel) bandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.terminals)
	collapse := func() (tea.Model, tea.Cmd) {
		m.bandExpanded, m.bandFocused = false, false
		return m, nil
	}
	switch msg.String() {
	case "esc", "/", "`", "left", "q":
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
		t := m.terminals[m.bandSel]
		if canOpenNewWindow() {
			// Open the host shell in its own window/tab so it doesn't hijack the
			// dashboard — matching how island shells and agent sessions attach.
			if err := m.openHostTermWindow(t.ID, t.Label); err != nil {
				m.lastError = err.Error()
			}
			return m, nil
		}
		m.connectTerminal = t.ID // no new-window backend: attach in place (resumes live tmux)
		return m, tea.Quit
	case "d", "X", "delete", "backspace":
		// Delete the selected terminal (kills its host tmux session) after a
		// confirm. Not on the "+ new terminal" row. d / Del / X / Backspace all
		// work — whichever the operator reaches for.
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
	// Pin the kind glyph to a fixed 2-cell slot so the name (and the type column
	// after it) line up regardless of the glyph's render width — ◆/■/❯ aren't all
	// one cell in every terminal font, which otherwise nudged headless rows
	// (e.g. openclaw) out of column.
	glyphSlot := lipgloss.NewStyle().Width(2).Render(agentGlyph(a))
	left := fmt.Sprintf("%s%s  %s", glyphSlot, name, metaStr)
	status, statusStyle := agentStatus(a)
	if status == "" {
		return left
	}
	// Align the status word to a fixed column so the states read as a column down
	// the list, regardless of how wide each row's type/uptime/presence meta is.
	// lipgloss.Width measures visible cells (ignores the ANSI in glyph/meta). Rows
	// wider than the column (e.g. a disambiguated name) overflow past it with a
	// minimum 2-space gap rather than colliding — the "within reason" exception.
	pad := agentStatusCol - lipgloss.Width(left)
	if pad < 2 {
		pad = 2
	}
	return left + strings.Repeat(" ", pad) + statusStyle.Render(status)
}

// subAgentRowText renders an agent-spawned sub-agent's list line: dim/italic
// (styleSubAgent) with a plain kind glyph and an "ephemeral" marker, so it reads
// as a transient child rather than a peer agent. Deliberately simpler than
// agentRowText — no status column — since sub-agents are secondary detail; the
// state word still rides along when known.
func subAgentRowText(a api.AgentInfo) string {
	glyph := glyphAgent
	if !a.Attachable {
		glyph = glyphHeadless
	}
	meta := a.Type
	if a.State != "stopped" && a.State != "exited" && !a.CreatedAt.IsZero() {
		meta += "  up " + timeAgo(a.CreatedAt)
	}
	line := fmt.Sprintf("%s %s  %s", glyph, agentDisplayName(a), meta)
	if a.Ephemeral {
		line += "  · ephemeral"
	}
	if status, _ := agentStatus(a); status != "" {
		line += "  " + status
	}
	return styleSubAgent.Render(line)
}

// agentStatusCol is the visible column the agent state word starts at, sized to
// clear a typical "<type>  up <age>  <presence>" meta (e.g. "claude-code  up
// 40m" + a presence dot). Wider rows overflow gracefully (see agentRowText).
const agentStatusCol = 40

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
	if r := m.currentRow(); r.kind == rowSecrets {
		body := styleTitle.Render(glyphSecrets+" Secrets") + "\n\n" +
			styleMuted.Render("Tokens this island's tools read from the environment —\nEXPO_TOKEN, NPM_TOKEN, API keys.") + "\n\n" +
			styleMuted.Render("Press ⏎ to view, add, or rotate. Values are never shown.") + "\n\n"
		// The caveat belongs where someone decides what to put in, not only in
		// the pane they see afterwards.
		body += styleMuted.Render("Agents in this island can read these. Keeps them out of\nyour repo, and gives one place to rotate and revoke.")
		return body
	}
	if m.currentRow().kind == rowAddAgent {
		return styleTitle.Render("+ Add agent") + "\n\n" +
			styleMuted.Render("Press ⏎ to add an agent to "+styleAccent.Render(m.selectedName())+styleMuted.Render(".\nClaude Code, Codex, a terminal, or a headless command."))
	}
	if m.detail == nil {
		if name := m.selectedName(); name != "" {
			return styleAccent.Render("⏳ loading " + name + "…")
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
		memLine := fmt.Sprintf("%s / %s", humanBytes(d.Stats.MemoryUsageBytes), humanBytes(d.Stats.MemoryLimitBytes))
		if pct, ok := memUsagePct(d.Stats); ok {
			if st, flag := nearCapStyle(pct); flag {
				memLine += st.Render(fmt.Sprintf("  %.0f%% ⚠ near cap", pct))
			} else {
				memLine += styleMuted.Render(fmt.Sprintf("  %.0f%%", pct))
			}
		}
		b.WriteString(fmt.Sprintf("memory:    %s\n", memLine))
		cpu := fmt.Sprintf("%.1f%%", d.Stats.CPUPercent)
		if st, flag := nearCapStyle(d.Stats.CPUPercent); flag {
			cpu = st.Render(fmt.Sprintf("%.1f%% ⚠", d.Stats.CPUPercent))
		}
		b.WriteString(fmt.Sprintf("cpu:       %s\n", cpu))
	}
	// Container health (a3 usage #3): only when unhealthy — an OOM kill or a
	// container-level restart is a crash signal distinct from a per-agent restart.
	if h := d.Health; h != nil && (h.OOMKilled || h.RestartCount > 0) {
		hp := []string{}
		if h.OOMKilled {
			hp = append(hp, "OOM-killed")
		}
		if h.RestartCount > 0 {
			hp = append(hp, fmt.Sprintf("%d container restart(s)", h.RestartCount))
		}
		if h.ExitCode != 0 {
			hp = append(hp, fmt.Sprintf("last exit %d", h.ExitCode))
		}
		b.WriteString("health:    " + styleErrored.Render("⚠ "+strings.Join(hp, " · ")) + "\n")
	}
	if d.Disk != nil && d.Disk.TotalBytes > 0 {
		diskLine := fmt.Sprintf("%s (ws %s · home %s)",
			humanBytes(uint64(d.Disk.TotalBytes)), humanBytes(uint64(d.Disk.WorkspaceBytes)),
			humanBytes(uint64(d.Disk.HomeBytes)))
		// %-of-cap when a disk cap is configured (resources.disk, e.g. "20G").
		if d.Resources != nil {
			if cap, ok := parseCapBytes(d.Resources.Disk); ok {
				pct := float64(d.Disk.TotalBytes) / float64(cap) * 100
				if st, flag := nearCapStyle(pct); flag {
					diskLine += st.Render(fmt.Sprintf("  %.0f%% ⚠ near cap", pct))
				} else {
					diskLine += styleMuted.Render(fmt.Sprintf("  %.0f%% of %s", pct, d.Resources.Disk))
				}
			}
		}
		b.WriteString(fmt.Sprintf("disk:      %s\n", diskLine))
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
	// Token/cost (a3 usage #1 [second]) — agent-reported, so present only for
	// adapters that report it (Claude Code today). cost_usd is hidden for an
	// unpriced model. "n/a" only for an AI agent that COULD report but hasn't —
	// never for types that don't, so we don't imply uniform coverage.
	switch {
	case a.Usage != nil:
		u := a.Usage
		line := fmt.Sprintf("%s tokens (in %s · out %s)", humanCount(u.TotalTokens), humanCount(u.InputTokens), humanCount(u.OutputTokens))
		if u.CostUSD != nil {
			line += fmt.Sprintf(" · $%.2f", *u.CostUSD)
		}
		b.WriteString(fmt.Sprintf("usage:     %s %s\n", line, styleMuted.Render("("+u.Source+" · "+timeAgo(u.AsOf)+" ago)")))
	case a.Type == "claude-code":
		b.WriteString("usage:     " + styleMuted.Render("n/a — no usage reported yet") + "\n")
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
	// Row 1: the primary "open something" actions. Row 2: settings + the ⏎ action
	// menu (which holds the per-row lifecycle/setup actions — hibernate, reset,
	// purge, rename, ssh setup, …) + globals. Those keys still work directly;
	// they're listed in the ⏎ menu and in [?] help.
	term := ""
	if m.hostTerminalsAvailable() {
		term = "   [/] host terminal"
	}
	keys1 := "[⏎] open agent(s)   [>] island shell" + term
	keys2 := "[s] settings   [m] actions   [H] server   [space] expand   [?] help   [q] quit"
	left := m.renderFooterLeft()
	pad1 := m.width - lipgloss.Width(keys1) - 2
	if pad1 < 1 {
		pad1 = 1
	}
	pad2 := m.width - lipgloss.Width(keys2) - 2
	if pad2 < 1 {
		pad2 = 1
	}
	keys1r := styleFooter.Render(keys1)
	// Row 1 carries a rotating help tip on the LEFT (a "did you know" nudge — e.g.
	// voice dictation for new users), with the key hints staying right-aligned.
	row1 := strings.Repeat(" ", pad1) + keys1r
	if tip := m.footerTipText(); tip != "" {
		if avail := m.width - lipgloss.Width(keys1) - 3; avail >= 24 {
			tipStr := styleMuted.Render(truncate(tip, avail))
			gap := m.width - lipgloss.Width(tipStr) - lipgloss.Width(keys1) - 2
			if gap < 1 {
				gap = 1
			}
			row1 = " " + tipStr + strings.Repeat(" ", gap) + keys1r
		}
	}
	return " " + left + "\n" +
		row1 + "\n" +
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
		// Muted grey reads as "nothing here", which is exactly wrong while the
		// daemon is being queried — the accent says something is happening.
		return styleAccent.Render("⏳ loading…")
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

// renderHelp draws the help overlay: every key, grouped into flat, always-visible
// sections (Island / Team / Server / TUI) — nothing hidden behind a toggle, so a
// control like [I] invite is discoverable straight from `?`.
// capitalizeFirst upper-cases the first letter of a help description so the line
// leads with a capital for skimming, leaving symbol-first rows (⏎, ↑/↓) alone.
func capitalizeFirst(s string) string {
	for i, r := range s {
		if r >= 'a' && r <= 'z' {
			return s[:i] + string(r-32) + s[i+1:]
		}
		if r >= 'A' && r <= 'Z' {
			return s // already capitalized
		}
		// A leading symbol/space: don't force a capital onto it.
		break
	}
	return s
}

func (m tuiModel) renderHelp() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Dejima — how to use it"))
	b.WriteString("\n\n")

	// The overlay renders into a bordered, single-padded pane spanning the width,
	// so the usable content is m.width - 4 (2 border + 2 padding). Truncate every
	// row to that so a narrow terminal clips with an … instead of wrapping — a wrap
	// would desync the scroll window's line accounting. Floor keeps it sane before
	// the first resize.
	contentW := m.width - 4
	if contentW < 24 {
		contentW = 24
	}

	sec := func(title string, rows [][2]string) {
		b.WriteString(styleHeader.Render(truncateDisplay(title, contentW)))
		b.WriteString("\n")
		for _, kv := range rows {
			key := runewidth.FillRight(kv[0], 9)          // display-width padding (bytes ≠ cols for ⏎/↑↓)
			prefixW := 2 + runewidth.StringWidth(key) + 2 // "  " + key + "  "
			desc := truncateDisplay(capitalizeFirst(kv[1]), contentW-prefixW)
			b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(key), styleMuted.Render(desc)))
		}
		b.WriteString("\n")
	}

	sec("Island controls", [][2]string{
		{"n", "new island — pick a repo (or paste a URL), choose an agent, launch"},
		{"⏎ / o", "island → opens all its agents (each in a tab); agent → its session; headless → its logs"},
		{">", "open a shell at /workspace inside the highlighted island (contained)"},
		{"space ←/→", "expand an island to its agents, the + add-agent row, and headless logs"},
		{"E", "expand / collapse all islands at once"},
		{"+", "add an agent — Claude Code, Codex, a terminal, or a headless command"},
		{"X", "remove the highlighted agent (the island can run with zero agents)"},
		{"e", "rename — island display title, or relabel an agent (cosmetic; the slug/id stay)"},
		{"v", "set an agent's LLM provider / model / key (key-requiring types)"},
		{"[ ]", "reorder the highlighted agent within its island (move up / down)"},
		{"a", "attach here — replaces the dashboard with the agent"},
		{"m", "actions menu for the highlighted row (attach, hibernate, rename, upgrade, ssh, purge…)"},
		{"c", "open the island in your editor over SSH, straight at /workspace"},
		{"h", "hibernate — stop the container, keep all data"},
		{"w", "wake a hibernated island"},
		{"r", "reset agent state (workspace preserved) — confirms first"},
		{"d", "purge — destroy the island and its volumes — confirms first"},
	})

	sec("Team controls", [][2]string{
		{"I", "invite a teammate — mint a role/owner-scoped token + copyable invite; list/revoke (owner-only)"},
		{"O", "owner lens — your islands (default) vs all islands on the daemon"},
		{"%", "host utilization — shared totals across all islands (no names)"},
		{"T", "grants — what the highlighted island can reach (Port · MCP · links · caps)"},
		{"P", "Port scopes — brokered host-file grants (add/revoke; deny-all by default)"},
		{"V", "approvals — review/approve/deny pending cross-island actions (the action gate)"},
		{"A", "audit ledger — chain-verification + recent governance activity"},
	})

	sec("Server controls (the daemon / host)", [][2]string{
		{"H", "server menu — update daemon · set up SSH fleet-wide · build image · refresh"},
		{"u / U", "update Dejima — the client first, then the daemon if needed (daemon update warns + gates: it restarts the daemon, closing all terminals fleet-wide). Also in [H]"},
		{"s / S", "settings — editor · group-by-repo · connection target (which server)"},
		{"/", "host terminals — the pinned band of (uncontained) shells on the daemon host; [t] adds one"},
		{"b", "build the island image on the daemon host — confirms first"},
		{"R", "refresh now"},
	})

	sec("TUI controls", [][2]string{
		{"↑/↓ j/k", "move between rows   ·   g/G jump to top/bottom"},
		{"PgUp/PgDn", "scroll the detail panel (events, agents) — Ctrl-u/Ctrl-d also work"},
		{"p", "group the island list by repo — multi-agent projects read as one"},
		{"#", "reveal / hide agent ids (names only by default)"},
		{"?", "this help   ·   q quit the dashboard"},
	})

	// Chords that work WHILE attached to an agent — intercepted by `dejima
	// connect` before the agent sees them, so they don't collide with the agent's
	// own keys. Documented here because they're otherwise invisible.
	sec("In an agent session", [][2]string{
		{"Ctrl-V", "paste a clipboard image to the agent (Alt-V too; DEJIMA_PASTE_KEY)"},
		{"Ctrl-]", "attach a local file — type/paste its path, it uploads (DEJIMA_ATTACH_KEY)"},
		{"Ctrl-\\", "summon this dashboard — the session stays alive (when launched from here)"},
		{"Ctrl-b d", "detach — the agent keeps running inside"},
	})

	// Everything above is keybindings — always visible. The rest is REFERENCE
	// (glyph legend + the scriptable CLI), collapsed by default into an [a] More
	// dropdown so the default `?` stays short and never hides a key.
	if !m.helpMore {
		// Terse so it fits a narrow pane (see TestHelpFitsWidth); the section
		// headers above already say what's here, and [a] reveals the rest.
		b.WriteString("\n")
		b.WriteString(styleAccent.Render("[a]") + styleMuted.Render(" more") + "   " +
			styleAccent.Render("[?/esc]") + styleMuted.Render(" close"))
		return b.String()
	}

	b.WriteString("\n")
	b.WriteString(styleAccent.Render("[a]") + styleMuted.Render(" ▾ less"))
	b.WriteString("\n\n")
	para := "An island = a contained workspace that can hold several agents sharing its\ncreds and git. ⏎ on an island opens all its agents (each in its own window); ⏎\non an agent opens just that one; > opens a shell at /workspace (inside the\ncontainer). Expand one with [space], then [+] add agents. Headless agents have\nno screen — ⏎ opens their logs."
	paraLines := strings.Split(para, "\n")
	for i, l := range paraLines {
		paraLines[i] = truncateDisplay(l, contentW)
	}
	b.WriteString(styleMuted.Render(strings.Join(paraLines, "\n")))
	b.WriteString("\n\n")
	// Tips — the full set of the one-liners that rotate in the header.
	b.WriteString(styleHeader.Render(truncateDisplay("Tips", contentW)))
	b.WriteString("\n")
	for _, tip := range dashboardTips {
		b.WriteString("  " + styleMuted.Render(truncateDisplay(tip, contentW-2)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(styleHeader.Render(truncateDisplay("Glyphs", contentW)))
	b.WriteString("\n  ")
	// These two lines mix styled spans, so we can't safely cut them mid-ANSI.
	// Fall back to a truncated, unstyled version when they'd overflow the pane
	// (losing color on a very narrow terminal beats a wrap that desyncs scroll).
	glyphLine := styleMuted.Render(fmt.Sprintf(
		"%s island   %s AI agent   %s shell   %s headless   ", "●", glyphAgent, glyphTerminal, glyphHeadless)) +
		styleAccent.Render("◉") + styleMuted.Render(" attached (someone's driving)")
	glyphPlain := fmt.Sprintf("%s island   %s AI agent   %s shell   %s headless   ◉ attached (someone's driving)",
		"●", glyphAgent, glyphTerminal, glyphHeadless)
	if runewidth.StringWidth(glyphPlain) > contentW-2 {
		glyphLine = styleMuted.Render(truncateDisplay(glyphPlain, contentW-2))
	}
	b.WriteString(glyphLine)
	b.WriteString("\n  ")
	stateLine := styleMuted.Render("color = state: ") +
		styleRunning.Render("working") + styleMuted.Render(" · ") +
		styleHibernate.Render("idle/stopped") + styleMuted.Render(" · ") +
		styleNeedsYou.Render("needs you") + styleMuted.Render(" · ") +
		styleErrored.Render("error")
	statePlain := "color = state: working · idle/stopped · needs you · error"
	if runewidth.StringWidth(statePlain) > contentW-2 {
		stateLine = styleMuted.Render(truncateDisplay(statePlain, contentW-2))
	}
	b.WriteString(stateLine)
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render(truncateDisplay("islands are uniform by default; give one its own color + glyph via the actions menu (m → Color & glyph)", contentW-2)))
	b.WriteString("\n\n")

	b.WriteString(styleHeader.Render(truncateDisplay("From the shell (scriptable; the TUI is just a front-end)", contentW)))
	b.WriteString("\n")
	shell := [][2]string{
		{"dejima init --repo <url|path>", "provision an island (--local-copy to seed unpushed work)"},
		{"dejima connect <name>", "attach a terminal into an island"},
		{"dejima ls / status <name>", "list islands / detail view"},
		{"dejima exec <name> -- <cmd>", "run a one-shot command inside an island"},
		{"dejima cp <src> <dst>", "copy files in or out"},
		{"dejima attach <name>[/<agent>] <path>", "attach a local file to an agent (in-session: " + attachKeyLabel() + ")"},
		{"dejima token invite --role operator --owner <who> --host <addr>", "onboard a scoped teammate (CLI twin of [I])"},
		{"dejima hibernate|wake|reset|purge", "lifecycle from the CLI"},
		{"dejima image build / upgrade <name>", "rebuild the island image / roll an island onto it"},
		{"dejima auth push / status", "send this machine's Claude login to the daemon host"},
		{"DEJIMA_HOST=host:7273 dejima …", "drive a remote daemon over your tailnet"},
	}
	// Command column is normally 58 cols, but clamp it on a narrow terminal so the
	// padded column + prefix can never exceed the pane (which would wrap).
	colW := 58
	if colW > contentW-4 {
		colW = contentW - 4
	}
	for _, kv := range shell {
		cmd := runewidth.FillRight(truncateDisplay(kv[0], colW), colW)
		prefixW := 2 + runewidth.StringWidth(cmd) + 2
		desc := truncateDisplay(kv[1], contentW-prefixW)
		b.WriteString(fmt.Sprintf("  %s  %s\n", styleAccent.Render(cmd), styleMuted.Render(desc)))
	}

	b.WriteString("\n")
	b.WriteString(styleAccent.Render("[?/esc]") + styleMuted.Render(" close"))
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
	// prompt = the question; input = what to type (its own bold line below the
	// question, so the "how to confirm" step is unmissable instead of buried at
	// the tail of a long sentence). A y/n verb sets input to the default "y".
	var prompt, input string
	switch c.verb {
	case "reset":
		prompt = fmt.Sprintf("Clear agent state for %q? (workspace preserved)", c.island)
	case "upgrade":
		prompt = fmt.Sprintf("Recreate %q on the current island image? (all state preserved)", c.island)
	case "recreate-island":
		prompt = fmt.Sprintf("OOM priority changed — restart %q now to apply? (recreates the container; workspace + agents preserved)", c.island)
	case "build-image":
		prompt = "Rebuild the island image? Takes a few minutes; islands pick it up on upgrade."
	case "purge":
		prompt = fmt.Sprintf("DESTROY %q, including all volumes. This cannot be undone.", c.island)
		input = "the island name (" + c.island + ")"
	case "force-purge":
		prompt = fmt.Sprintf("%q has unpushed/uncommitted work that will be LOST. Force-purge anyway?", c.island)
	case "remove-agent":
		who := c.agent
		if isl, ok := m.islandByName(c.island); ok {
			if lbl := agentByID(isl, c.agent).Label; lbl != "" {
				who = lbl
			}
		}
		prompt = fmt.Sprintf("Remove agent %q (id %s) from island %q — destroys its worktree + agent state.", who, c.agent, c.island)
		input = "the agent id (" + c.agent + ")"
	case "remove-secret":
		prompt = fmt.Sprintf("Remove secret %q from island %q — tools using it will start failing.", c.agent, c.island)
		input = "the secret name (" + c.agent + ")"
	case "remove-terminal":
		prompt = fmt.Sprintf("Close host terminal %s? (kills the shell on the daemon host)", c.agent)
	case "approve-action":
		prompt = fmt.Sprintf("⚠ Approve this DESTRUCTIVE cross-island action (%s)? It runs once approved.", c.agent)
	case "deny-action":
		prompt = fmt.Sprintf("Deny action %s.", c.agent)
		input = "an optional reason (or leave blank)"
	case "approve-rule":
		prompt = fmt.Sprintf("Approve %s AND auto-approve this link+action going forward.", c.agent)
		input = "'<max> [<ttl>]' (e.g. '20 1h'; blank = unlimited, no expiry)"
	case "open-all-agents":
		prompt = fmt.Sprintf("Open all %d agents of %q in separate windows?", len(m.attachableAgentIDs(c.island)), c.island)
	case "relabel-agent":
		prompt = fmt.Sprintf("Rename agent %s.", c.agent)
		input = "a name (blank clears the label)"
	case "rename-island":
		prompt = fmt.Sprintf("Rename %q.", c.island)
		input = "a display title (blank resets to the name)"
	case "setup-ssh":
		prompt = "Authorize this machine's SSH key for ALL islands and add ~/.ssh/config entries for VS Code/Cursor?"
	case "update-client":
		prompt = fmt.Sprintf("Download %s and replace this dejima binary? (verified against the release checksums)", m.latestRelease)
	case "update-daemon":
		if c.force {
			// The daemon deferred because clients are attached; forcing disconnects
			// them. This is a DISTINCT decision from the first confirm — say so, or
			// it reads as the same prompt asked twice.
			prompt = fmt.Sprintf("The daemon held off — attached terminal(s) would be disconnected. Force the update to %s and restart now?", m.latestRelease)
		} else {
			prompt = fmt.Sprintf("Update the daemon to %s? This RESTARTS the daemon and closes ALL open terminals fleet-wide — containers and agents keep running, you just reattach.", m.latestRelease)
		}
	}
	// Render inside the centered styleMenuBox (View supplies the border): a clear
	// title, the question, a BOLD input line, the typed answer with a cursor, and
	// a key hint — so the confirm is an unmissable pop-up and "how to say yes" is
	// obvious, not buried.
	title := styleHeader.Render("Confirm")
	switch c.verb {
	case "purge", "force-purge", "remove-agent", "remove-terminal":
		title = styleErrored.Render("⚠  Confirm")
	}
	// Wrap the question so a long one doesn't run off the box.
	width := m.width - 10
	if width > 76 {
		width = 76
	}
	if width < 24 {
		width = 24
	}
	question := lipgloss.NewStyle().Width(width).Render(prompt)

	// The action line: for a y/n verb, "▸ Type  y  then Enter"; for a typed verb,
	// name what to type. The typed answer + cursor sit right after it.
	verb := "Type"
	what := input
	if what == "" {
		what = "y"
	}
	action := styleAccent.Render("▸ "+verb+" ") +
		styleTitle.Render(what) +
		styleAccent.Render(" then press Enter")
	answerLine := styleHeader.Render("  › ") + styleTitle.Render(c.answer+"▌")

	hint := styleMuted.Render("Enter = confirm    ·    Esc = cancel")
	return title + "\n\n" + question + "\n\n" + action + "\n" + answerLine + "\n\n" + hint
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
		if i == am.sel && !it.disabled {
			mark = styleAccent.Render(" ▸ ")
		}
		st := lipgloss.NewStyle()
		switch {
		case it.disabled:
			st = styleMuted
		case i == am.sel:
			st = styleSelected
		case it.danger:
			st = styleErrored
		}
		// Only menu items backed by a global hotkey advertise a "[key]"; menu-only
		// (open-func) and disabled lines omit the empty bracket.
		accel := ""
		if it.key != "" {
			accel = styleMuted.Render("  [" + it.key + "]")
		}
		b.WriteString(mark + st.Render(it.label) + accel + "\n")
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
	switch {
	case m.clientUpdate:
		updateRow = "Update                    " + styleWaiting.Render("→ "+m.latestRelease)
	case m.daemonUpdate:
		updateRow = "Update                    " + styleWaiting.Render("daemon → "+m.latestRelease+" (restarts daemon)")
	}
	row(0, "", "Preferred editor          "+styleMuted.Render(editorLabel)+styleMuted.Render("  →"))
	row(1, "", "Group islands by repo     "+styleMuted.Render(groupState))
	row(2, "", "Connection target         "+styleMuted.Render(target)+styleMuted.Render("  →"))
	row(3, "", "Team & invites            "+styleMuted.Render("invite a teammate, revoke access")+styleMuted.Render("  →"))
	row(4, "", "Check for updates")
	row(5, "", updateRow)
	githubRow := "GitHub                    "
	if miss := m.githubMissingCredIslands(); len(miss) > 0 {
		githubRow += styleWaiting.Render(fmt.Sprintf("⚠ %d island(s) need reconnect", len(miss))) + styleMuted.Render("  →")
	} else {
		githubRow += styleMuted.Render("connect your GitHub for private repos") + styleMuted.Render("  →")
	}
	row(6, "", githubRow)
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("↑/↓ move · ⏎ select · esc close"))
	return b.String()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// islandIdentityColors / islandIdentityGlyphs are the palette the visual-identity
// EDITOR offers (see tui_identity.go). They are NOT auto-assigned: islands look
// uniform by default (see islandIdentityDefault), and a color/glyph is opt-in per
// island via the actions menu. Colors are light-medium so they show on both the
// default dark background and the selected-row highlight, and deliberately avoid
// the state hues (green/amber/red); glyphs avoid the lifecycle glyphs.
var islandIdentityColors = []lipgloss.Color{
	"#ffffff", // the default (uniform) — first so the editor opens on it
	"#60a5fa", "#a78bfa", "#22d3ee", "#f472b6", "#2dd4bf",
	"#e879f9", "#38bdf8", "#818cf8", "#f0abfc", "#5eead4",
}

var islandIdentityGlyphs = []string{"◆", "▲", "★", "■", "◈", "✦", "♦", "⬟"}

// islandIdentityDefaultColor / Glyph are the uniform default every island wears
// until the operator sets one: white + a neutral glyph. (The per-island
// hash-distinct coloring was dropped — it carried no meaning; distinctness is
// now opt-in.)
const islandIdentityDefaultColor = "#ffffff"

var islandIdentityDefaultGlyph = islandIdentityGlyphs[0] // ◆

// islandIdentity returns the uniform default identity (white + neutral glyph) —
// the same for every island. A per-island override comes from islandVisual when
// the operator has set one.
func islandIdentity(name string) (lipgloss.Style, string) {
	_ = name // uniform default; name no longer hashed into a distinct color/glyph
	return lipgloss.NewStyle().Foreground(lipgloss.Color(islandIdentityDefaultColor)), islandIdentityDefaultGlyph
}

// islandVisual is the read-through used everywhere the dashboard draws an
// island's identity: the operator's stored override (isl.Identity, set via the
// editor / PUT) when present and valid, otherwise the deterministic per-name
// default. One seam so a stored identity and the default render identically.
func islandVisual(isl api.IslandInfo) (lipgloss.Style, string) {
	if isl.Identity != nil && isl.Identity.Glyph != "" && isl.Identity.Color != "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(isl.Identity.Color)), isl.Identity.Glyph
	}
	return islandIdentity(isl.Name)
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
		// Flag memory pressure on the row so the fleet shows a runaway agent at a
		// glance (a3 usage #1) — only when near the cap, else the row stays quiet.
		if pct, ok := memUsagePct(isl.Stats); ok {
			if st, flag := nearCapStyle(pct); flag {
				parts = append(parts, st.Render(fmt.Sprintf("mem %.0f%% ⚠", pct)))
			}
		}
	}
	// Per-agent type belongs on each agent row, not here — an island's first
	// agent's type says nothing about the rest. (See agentRowText.)
	return strings.Join(parts, " · ")
}

// memUsagePct returns an island's memory use as a percent of its limit, and
// whether a usable figure exists (limit > 0). The cgroup limit is the real
// ceiling, so this approaching 100% is the runaway-agent signal (a3 usage #1).
func memUsagePct(s *api.IslandStats) (float64, bool) {
	if s == nil || s.MemoryLimitBytes == 0 {
		return 0, false
	}
	return float64(s.MemoryUsageBytes) / float64(s.MemoryLimitBytes) * 100, true
}

// parseCapBytes parses a configured size cap like "20G" / "512m" / "2g" into
// bytes (binary units, matching humanBytes). ok=false for empty/unparseable —
// callers then skip the %-of-cap and just show the raw usage.
func parseCapBytes(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	mult := uint64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	case 't', 'T':
		mult = 1 << 40
	}
	if mult > 1 {
		s = strings.TrimSpace(s[:len(s)-1])
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return uint64(n * float64(mult)), true
}

// nearCapStyle flags a usage percent: red at/over 90 (about to OOM), amber
// at/over 75, else not flagged (ok=false → render it plainly).
func nearCapStyle(pct float64) (lipgloss.Style, bool) {
	switch {
	case pct >= 90:
		return styleErrored, true
	case pct >= 75:
		return styleWaiting, true
	default:
		return lipgloss.Style{}, false
	}
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

// truncateDisplay shortens s to fit w terminal columns measured by display width
// (not bytes), appending … when it clips. Rune- and wide-char-aware, so it never
// splits a multibyte glyph (·, —, ⏎) mid-sequence. Operate on UNSTYLED text —
// truncate before applying lipgloss styles. Introducing no wraps keeps the help
// overlay's line count stable, so the scroll window stays in sync.
func truncateDisplay(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	target := w - 1 // reserve a column for the ellipsis
	var b strings.Builder
	width := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if width+rw > target {
			break
		}
		b.WriteRune(r)
		width += rw
	}
	b.WriteRune('…')
	return b.String()
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
			styleMuted.Render(timeAgo(e.Timestamp)), eventSummary(e)))
	}
}

// _ = exec to avoid an unused-import error if we change the connect path later.
var _ = exec.Command
