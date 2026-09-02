package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/reposrc"
)

type creatorStep int

const (
	stepRoot            creatorStep = iota // first-load: choose a directory to scan
	stepPick                               // pick a discovered repo (or switch to manual)
	stepManual                             // type a URL or path
	stepGitHub                             // browse a daemon GitHub identity's repos
	stepSource                             // diverged local repo: clone origin vs local copy
	stepAgent                              // choose an agent (primary, then any extras)
	stepAgentName                          // name that agent (blank = use its id)
	stepAgentKey                           // set the provider key a key-requiring agent needs (guided)
	stepAgents                             // roster: review seeded agents, add more, or continue
	stepName                               // confirm/edit the island name
	stepCreate                             // provisioning in flight
	stepGitHubGate                         // create refused: private repo needs a GitHub identity — guided connect
	stepGitHubPreflight                    // pasted a GitHub URL with no identity connected — warn BEFORE building
	stepFromDir                            // type the host folder to seed /workspace from
)

// ghBrowsePhase tracks the two steps of the daemon-backed GitHub browser.
type ghBrowsePhase int

const (
	ghPickIdentity ghBrowsePhase = iota // choose which daemon GitHub identity
	ghPickRepo                          // choose one of that identity's repos
)

// creatorModel holds the state of the new-island flow. It is owned by tuiModel
// as a pointer (nil when inactive) and mutated in place.
type creatorModel struct {
	client      *api.Client
	daemonLocal bool
	callerRole  string           // "owner"|"operator"|"viewer"|"" — gates how GitHub-connect guidance is phrased (adding an identity is owner-only)
	existing    []api.IslandInfo // for name disambiguation + per-repo island counts

	step creatorStep
	err  string

	// root prompt
	rootChoices []string
	rootCursor  int
	rootTyping  bool
	rootInput   string

	// repo picker
	root        string
	scanning    bool
	repos       []reposrc.Repo
	repoCursor  int
	statusCache map[string]reposrc.Status

	// manual entry
	manualInput string

	// GitHub browse: pick a daemon identity, then one of its repos. The chosen
	// identity rides onto the create request so the island clones/pushes as it.
	ghPhase      ghBrowsePhase
	ghIdentities []githubid.Meta
	// ghIdentitiesLoaded distinguishes "no identities" from "never asked". Only
	// the first justifies a warning; the second must stay silent, because a
	// false warning on the happy path is how a gate gets ignored on the unhappy
	// one. See tui_create_preflight.go.
	ghIdentitiesLoaded bool
	preflightName      string // island name derived from the pasted URL, held across the preflight
	ghIdentity         string // chosen identity name → CreateIslandRequest.GitHubIdentity
	ghIdentCur         int
	ghRepos            []githubid.Repo
	ghRepoCur          int
	ghCapped           bool // identity sees more repos than the page we fetched
	ghLoading          bool
	ghHint             string // shown when the daemon has no identities

	// source-divergence prompt
	pendingPath   string
	pendingOrigin string
	pendingAhead  int
	sourceCursor  int

	// agent naming: the type/cmd chosen at stepAgent, held until the label is
	// entered at stepAgentName and the pair is appended to agents together.
	pendingAgent api.AgentSpecRequest
	agentNameIn  string

	// guided provider-key step (stepAgentKey): shown when the chosen agent needs
	// a provider key and none is configured, so the agent launches WITH it rather
	// than coming up broken. keyProviders is the type's supported providers.
	keyProviders []string
	keyProvSel   int
	keyInput     string
	keyBusy      bool
	keySetOK     map[string]bool // providers set during this flow (don't re-prompt)

	// noRepo marks the deliberate empty-workspace branch: no URL, no local path,
	// no seed. It skips the repo-source steps entirely rather than feeding them ""
	// and relying on each to decline.
	noRepo bool
	// fromDir seeds /workspace from a host folder that is not a repo. Like
	// noRepo it skips the repo-source steps, but unlike noRepo the workspace is
	// not empty afterwards — the island is repo-less, not blank.
	fromDir      string
	fromDirInput string
	fromDirGit   bool // run `git init` after seeding — explicit, never implied

	// resolved selection
	resolution   reposrc.Resolution
	picker       agentPicker            // agent type (and headless command) chooser
	keyGap       map[string]bool        // agent types needing an unconfigured LLM key (picker annotation)
	agents       []api.AgentSpecRequest // seeded agents; element 0 is the primary
	pickingExtra bool                   // true while the picker is adding a non-primary agent
	nameInput    string
	creating     bool
	// gateRepo/forceNoIdentity drive the guided first-clone step: when a create is
	// refused because a private repo has no GitHub identity, gateRepo names it and
	// the flow moves to stepGitHubGate; forceNoIdentity retries with the override.
	gateRepo        string
	forceNoIdentity bool
	// imageMissing is true when the daemon has no island image yet, so this
	// create will trigger a one-time multi-minute base-image build. Used to set
	// that expectation up front instead of leaving the user staring at a silent
	// "provisioning…". Only set when we positively know (overview loaded).
	imageMissing bool
}

// --- messages -------------------------------------------------------------

type reposDiscoveredMsg struct {
	root  string
	repos []reposrc.Repo
	err   error
}
type repoStatusMsg struct {
	path   string
	status reposrc.Status
}
type islandCreatedMsg struct {
	name string
	// primary agent (element 0), so the auto-open attaches straight into it
	// instead of landing on the multi-agent `connect` picker.
	agentID    string
	agentLabel string
	err        error
}
type ghIdentitiesMsg struct {
	identities []githubid.Meta
	err        error
}
type ghReposMsg struct {
	repos  []githubid.Repo
	capped bool
	err    error
}

// --- entry / commands -----------------------------------------------------

// openCreator initializes the new-island flow: straight to the picker if a
// scan root is already configured, otherwise to the first-load root prompt.
func (m tuiModel) openCreator() (tea.Model, tea.Cmd) {
	cfg, _ := clientcfg.Load()
	c := &creatorModel{
		client:      m.client,
		daemonLocal: resolveHost() == "",
		callerRole:  m.callerRole,
		existing:    m.islands,
		statusCache: map[string]reposrc.Status{},
		// "Start empty" now leads the list, so the cursor's ZERO VALUE would make
		// `n` then ⏎ create an empty island — the trap #355 added a test for,
		// re-armed by reordering. The default action stays the primary path.
		repoCursor:   pickRowGitHub,
		imageMissing: m.overview != nil && !m.overview.IslandImagePresent,
		keyGap:       m.agentKeyGap,
	}
	m.creator = c
	if m.demo {
		// Site recording: a fixed synthetic repo list, no filesystem scan (which
		// would leak the operator's real repos), no real create.
		c.step, c.root, c.scanning = stepPick, "~/code", false
		c.repos = demoRepos()
		return m, nil
	}
	if cfg.RepoRoot == "" {
		pwd, _ := os.Getwd()
		c.step = stepRoot
		// GitHub is a first-class source here (not just after a local scan): a
		// teammate driving a REMOTE daemon has no useful local repos to scan, so
		// burying "Browse my GitHub repos" behind a scan hid the option they most
		// needed. See viewPick — the same choice also lives there post-scan.
		// "Start empty" LEADS, and it is the only row here that always works.
		//
		// This screen is what a fresh client shows before any repo root is
		// configured, and it used to offer four ways to name a repo and no way to
		// skip having one. An operator with no repo yet — or on Windows, where the
		// two directory rows name CLIENT paths a WSL/remote daemon cannot use —
		// had nothing on this screen they could complete. Reported from a fresh
		// Windows install as "no ready way to set up an empty repo or copy some
		// files over": the empty option existed the whole time, on the screen
		// AFTER a scan they had no reason to run.
		//
		// It leads for the same reason it leads in viewPick, and carries the same
		// hazard: the cursor's zero value decides what Enter-Enter does. #355
		// added a test for exactly that; rootCursor is set below to match.
		// THREE SOURCES, named by what the island STARTS FROM rather than by how
		// you go looking for a repo.
		//
		// It was five — start empty, scan this directory, choose another
		// directory, browse GitHub, enter a URL — which is two questions
		// interleaved: what should be in /workspace, and how do I find it. The
		// operator's own framing was better and is what this now uses: clone a
		// repo, use a local one, or start with nothing. Finding is a detail
		// INSIDE the first two, not a peer of them.
		c.rootChoices = rootSourceChoices(tildeify(pwd))
		// Do not let the zero value pick the destructive-by-surprise option. An
		// empty island is cheap and reversible, so leading with it is safe — but
		// the cursor is set explicitly rather than left at 0 by accident, so the
		// next person to reorder the rows has to make the choice on purpose.
		// Clone leads: it is what most people want, and it is the only one of the
		// three that cannot be reached later from inside the others.
		c.rootCursor = rootRowClone
		return m, nil
	}
	c.step = stepPick
	c.root = cfg.RepoRoot
	c.scanning = true
	return m, discoverCmd(cfg.RepoRoot)
}

func discoverCmd(root string) tea.Cmd {
	return func() tea.Msg {
		repos, err := reposrc.Discover(root, 2)
		return reposDiscoveredMsg{root: root, repos: repos, err: err}
	}
}

func repoStatusCmd(path string) tea.Cmd {
	return func() tea.Msg {
		return repoStatusMsg{path: path, status: reposrc.GetStatus(path)}
	}
}

// buildRequest assembles the create request from the resolved selection and the
// seeded agent roster. A single agent uses the scalar Agent/Cmd fields (request
// identical to the pre-multi-seed flow); extras populate Agents (element 0 is
// the primary, mirrored into the scalar fields too).
func (c *creatorModel) buildRequest() api.CreateIslandRequest {
	req := api.CreateIslandRequest{
		Name:            c.nameInput,
		Repo:            c.resolution.Repo,
		SeedPath:        c.resolution.SeedPath,
		NoRepo:          c.noRepo,
		FromDir:         c.fromDir,
		GitInit:         c.fromDirGit && c.fromDir != "",
		Agent:           c.agents[0].Type,  // primary (scalar back-compat path)
		Cmd:             c.agents[0].Cmd,   // headless only; empty for interactive agents
		GitHubIdentity:  c.ghIdentity,      // "" unless sourced via the GitHub browser
		AllowNoIdentity: c.forceNoIdentity, // set by the guided-gate "create anyway" path
	}
	// Always send the roster when there is one — not just for multi-agent
	// islands. The scalar Agent/Cmd fields above cannot carry a Label, so
	// gating on len>1 silently dropped the name for a single agent, which is
	// the common case: the island came up labelled by type ("claude") and the
	// name the operator typed was lost between the form and the request.
	if len(c.agents) > 0 {
		req.Agents = c.agents
	}
	return req
}

func (c *creatorModel) createCmd() tea.Cmd {
	client := c.client
	req := c.buildRequest()
	return func() tea.Msg {
		// Auto-build the island image when the daemon doesn't have it yet
		// (fresh host) — first island creation Just Works, it just takes the
		// build's few extra minutes.
		// Ask the daemon whether Docker is even up before spending minutes on a
		// build that cannot succeed. This overview call already happened; only
		// IslandImagePresent was being read from it, so the answer was fetched
		// and discarded while the operator waited for the inevitable failure.
		o, overviewErr := client.Overview(context.Background())
		if overviewErr == nil && !o.DockerReachable {
			return islandCreatedMsg{err: dockerUnreachableError(client.DaemonHost())}
		}
		if overviewErr == nil && !o.IslandImagePresent {
			bctx, bcancel := context.WithTimeout(context.Background(), 30*time.Minute)
			// Keep the tail. io.Discard here meant a failed build surfaced as
			// "docker build failed: exit status 1" with docker's own explanation
			// thrown away — an exit code is not a bug report, and the operator
			// hitting it is usually not the one who can read the daemon's logs.
			tail := newBuildTail(40)
			err := client.BuildImage(bctx, tail)
			bcancel()
			if err != nil {
				if out := tail.String(); out != "" {
					return islandCreatedMsg{err: fmt.Errorf("build island image: %w\n\n%s", err, out)}
				}
				return islandCreatedMsg{err: fmt.Errorf("build island image: %w\n(no output was captured)", err)}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		info, err := client.CreateIsland(ctx, req)
		if err != nil {
			return islandCreatedMsg{err: err}
		}
		msg := islandCreatedMsg{name: info.Name}
		if len(info.Agents) > 0 { // element 0 is the primary agent
			msg.agentID = info.Agents[0].ID
			msg.agentLabel = info.Agents[0].Label
		}
		return msg
	}
}

// --- async message handlers ----------------------------------------------

func (c *creatorModel) onReposDiscovered(msg reposDiscoveredMsg) {
	c.scanning = false
	if msg.err != nil {
		c.err = "scan failed: " + msg.err.Error()
		return
	}
	c.repos = msg.repos
	// repoCursor is a ROW index and includes the leading action rows, so comparing
	// it against the repo COUNT is wrong: with two repos found, a cursor resting on
	// the first of them (pickRowFirstRepo) would test 4 >= 2 and jump to the top,
	// moving the selection out from under the user as the scan lands.
	if last := pickRowFirstRepo + len(c.repos) - 1; c.repoCursor > last {
		c.repoCursor = pickRowGitHub
	}
}

func (c *creatorModel) onRepoStatus(msg repoStatusMsg) {
	c.statusCache[msg.path] = msg.status
}

// --- key handling ---------------------------------------------------------

func (m tuiModel) creatorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	// Global escape hatch (except while typing, where esc cancels the field).
	if msg.String() == "ctrl+c" {
		m.creator = nil
		return m, nil
	}
	switch c.step {
	case stepRoot:
		return m.creatorRootKey(msg)
	case stepPick:
		return m.creatorPickKey(msg)
	case stepManual:
		return m.creatorManualKey(msg)
	case stepGitHub:
		return m.creatorGitHubKey(msg)
	case stepSource:
		return m.creatorSourceKey(msg)
	case stepAgent:
		return m.creatorAgentKey(msg)
	case stepAgentName:
		return m.creatorAgentNameKey(msg)
	case stepAgentKey:
		return m.creatorProviderKeyKey(msg)
	case stepAgents:
		return m.creatorAgentsKey(msg)
	case stepName:
		return m.creatorNameKey(msg)
	case stepGitHubPreflight:
		return m.creatorGitHubPreflightKey(msg)
	case stepGitHubGate:
		return m.creatorGitHubGateKey(msg)
	case stepFromDir:
		return m.creatorFromDirKey(msg)
	}
	return m, nil // stepCreate: ignore input while provisioning
}

// creatorGitHubGateKey drives the guided first-clone gate (a private repo with no
// GitHub identity). [c] launches the guided connect (in its own window); Enter
// retries the create once connected; [f] creates anyway (authenticate later);
// esc cancels.
func (m tuiModel) creatorGitHubGateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	// Lowercase only. Uppercase C is the tail byte of the right-arrow sequence
	// (ESC [ C), so on a terminal that delivers a sequence as separate
	// keypresses, pressing Right lands here.
	case "c":
		// --default: this gate only fires when NO identity resolves, so the one
		// being created is the one everything should follow. Leaving it implicit
		// is how a daemon ends up with identities and no default.
		// HANDS OFF TO THE GITHUB PANE, which runs the device flow in-process
		// (tui_github_device.go). This used to spawn a terminal window and then
		// say "opened … approve it on GitHub" — a claim it could not check. On
		// Windows the window died instantly and the sentence stayed on screen,
		// so the pane's most confident line was its least reliable one.
		//
		// The creator closes because the create it was mid-way through has
		// already been refused; there is no in-progress state worth preserving,
		// and pretending otherwise is how a wizard resumes into a stale answer.
		m.creator = nil
		return m.openGithubViewConnecting("")
	case "enter", "r": // not "R": ESC O R is F3
		c.creating, c.step, c.err = true, stepCreate, ""
		return m, c.createCmd()
	// not "F": ESC [ F is End — and this branch creates the island with NO
	// GitHub identity, which is too consequential to be reachable from an arrow.
	case "f":
		c.forceNoIdentity = true
		c.creating, c.step, c.err = true, stepCreate, ""
		return m, c.createCmd()
	case "esc", "ctrl+[", "q":
		m.creator = nil
		return m, nil
	}
	return m, nil
}

// isGitHubIdentityGateError reports whether a create failed the daemon's
// private-repo identity gate (blockDoomedClone) — the signal that upgrades the
// bare error into the guided connect step. Matched on the stable phrase the gate
// emits; keep in sync with blockDoomedClone in internal/api/create_identity_gate.go.
func isGitHubIdentityGateError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "needs a GitHub identity to clone")
}

func (m tuiModel) creatorRootKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	if c.rootTyping {
		switch msg.String() {
		case "esc", "ctrl+[":
			c.rootTyping = false
		case "enter":
			dir := strings.TrimSpace(c.rootInput)
			if dir == "" {
				return m, nil
			}
			_ = clientcfg.Save(clientcfg.Config{RepoRoot: dir})
			c.root, c.step, c.scanning = dir, stepPick, true
			return m, discoverCmd(dir)
		case "backspace":
			if c.rootInput != "" {
				c.rootInput = c.rootInput[:len(c.rootInput)-1]
			}
		default:
			c.rootInput += msg.String()
		}
		return m, nil
	}
	switch msg.String() {
	// ctrl+[ alongside esc: they are the SAME BYTE, and on Windows Terminal the
	// operator reported esc not registering while q did. Rather than guess at
	// that input layer a third time, accept both spellings everywhere.
	case "esc", "ctrl+[", "q":
		m.creator = nil
	case "/":
		// Reachable from the top screen too, not only after a scan: someone whose
		// repo is neither on GitHub nor in this directory should not have to run
		// a scan first to find the row that lets them type a path.
		c.step, c.manualInput, c.err = stepManual, "", ""
	case "up", "k":
		if c.rootCursor > 0 {
			c.rootCursor--
		}
	case "down", "j":
		if c.rootCursor < len(c.rootChoices)-1 {
			c.rootCursor++
		}
	case "enter":
		switch c.rootCursor {
		case rootRowClone:
			return m.creatorEnterGitHub()
		case rootRowLocal:
			// Scan where they are. "Choose another directory" and "type a URL or
			// path" are still reachable from the results screen ([/]), which is
			// where someone who does not find what they wanted actually is.
			pwd, _ := os.Getwd()
			_ = clientcfg.Save(clientcfg.Config{RepoRoot: pwd})
			c.root, c.step, c.scanning = pwd, stepPick, true
			return m, discoverCmd(pwd)
		case rootRowEmpty:
			return m.creatorEnterNoRepo()
		}
	}
	return m, nil
}

func (m tuiModel) creatorPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	// Two remote-source action rows lead, then the discovered local repos.
	lastRow := pickRowFirstRepo + len(c.repos) - 1
	if len(c.repos) == 0 {
		// No local repos discovered: the last selectable row is "start empty",
		// which sits below the two remote-source actions.
		lastRow = pickRowFromDir
	}
	switch msg.String() {
	case "esc", "ctrl+[", "q":
		m.creator = nil
	case "/":
		c.step, c.manualInput, c.err = stepManual, "", ""
	case "up", "k":
		if c.repoCursor > 0 {
			c.repoCursor--
		}
		return m, c.ensureStatus()
	case "down", "j":
		if c.repoCursor < lastRow {
			c.repoCursor++
		}
		return m, c.ensureStatus()
	case "enter":
		return m.creatorPickEnter()
	}
	return m, nil
}

// creatorPickEnter acts on the highlighted picker row: a discovered repo, the
// "enter a URL" action, or the "browse GitHub" action.
func (m tuiModel) creatorPickEnter() (tea.Model, tea.Cmd) {
	c := m.creator
	switch c.repoCursor {
	case pickRowManual:
		c.step, c.manualInput, c.err = stepManual, "", ""
		return m, nil
	case pickRowGitHub:
		return m.creatorEnterGitHub()
	case pickRowNoRepo:
		return m.creatorEnterNoRepo()
	case pickRowFromDir:
		c.step, c.fromDirInput, c.err = stepFromDir, "", ""
		return m, nil
	default:
		i := c.repoCursor - pickRowFirstRepo
		if i < 0 || i >= len(c.repos) {
			return m, nil
		}
		return m.creatorSelectRepo(c.repos[i])
	}
}

// ensureStatus lazily fetches working-tree status for the highlighted repo. No-op
// when the cursor is on one of the trailing action rows.
func (c *creatorModel) ensureStatus() tea.Cmd {
	i := c.repoCursor - pickRowFirstRepo
	if i < 0 || i >= len(c.repos) {
		return nil
	}
	p := c.repos[i].Path
	if _, ok := c.statusCache[p]; ok {
		return nil
	}
	return repoStatusCmd(p)
}

// creatorEnterNoRepo takes the empty-workspace branch: there is no URL, no local
// path and no seed, so every repo-source step (manual entry, the GitHub browser,
// the divergence prompt) has nothing to do and is skipped outright.
//
// It routes through stepName rather than straight to the agent picker, because a
// repo-less island has nothing to derive a name FROM. That mirrors the CLI, which
// refuses `--no-repo` without `--name` for the same reason: silently generating one
// produces islands nobody can predict the name of. stepName already rejects an empty
// name and validates the rest, so the requirement costs no new code — but the field
// has to START empty. uniqueName("") would hand back "island" (DeriveNameFromRepo's
// fallback), which is exactly the auto-generated name the CLI declines to invent.
func (m tuiModel) creatorEnterNoRepo() (tea.Model, tea.Cmd) {
	c := m.creator
	c.noRepo = true
	// Same Note the CLI prints, so both surfaces say the same thing. It is rendered
	// at every later step, which is what keeps "empty on purpose" visible rather
	// than leaving an empty /workspace looking like a clone that failed.
	c.resolution = reposrc.Resolution{Note: "no repo — /workspace starts empty"}
	// Initialise the picker/roster first: stepName's enter goes to stepAgent
	// directly, and would otherwise arrive with an unbuilt picker.
	mm, cmd := m.creatorEnterAgent("")
	c.nameInput = ""
	c.err = ""
	c.step = stepName
	return mm, cmd
}

// creatorSelectRepo decides whether a divergence prompt is warranted, else
// resolves immediately and advances to agent selection.
func (m tuiModel) creatorSelectRepo(repo reposrc.Repo) (tea.Model, tea.Cmd) {
	c := m.creator
	st := c.statusCache[repo.Path]
	// Only the local-daemon + has-origin + unpushed-commits case is a real fork.
	if c.daemonLocal && repo.Origin != "" && st.Ahead > 0 {
		c.pendingPath, c.pendingOrigin, c.pendingAhead = repo.Path, repo.Origin, st.Ahead
		c.sourceCursor, c.step = 0, stepSource
		return m, nil
	}
	res, err := reposrc.Resolve(repo.Path, c.daemonLocal, false)
	if err != nil {
		c.err = err.Error()
		return m, nil
	}
	c.resolution = res
	return m.creatorEnterAgent(repo.Name)
}

func (m tuiModel) creatorManualKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		if c.root != "" {
			c.step, c.err = stepPick, ""
		} else {
			m.creator = nil
		}
	case "enter":
		in := strings.TrimSpace(c.manualInput)
		if in == "" {
			return m, nil
		}
		res, err := reposrc.Resolve(in, c.daemonLocal, false)
		if err != nil {
			c.err = err.Error()
			return m, nil
		}
		c.resolution, c.err = res, ""
		// Ask the GitHub question HERE, on the screen where the repo was chosen,
		// rather than after an island has been built and an image pulled. Only the
		// pasted-URL path needs it: the browser path names the identity whose repos
		// it listed, so the credential that found the repo is the one the island
		// gets. See tui_create_preflight.go.
		c.preflightName = project.DeriveNameFromRepo(in)
		if !c.ghIdentitiesLoaded {
			c.step, c.ghLoading = stepGitHubPreflight, true
			return m, c.ghIdentitiesCmd()
		}
		if creatorPreflightGitHub(res.Repo, c.ghIdentities, c.ghIdentitiesLoaded) {
			c.step = stepGitHubPreflight
			return m, nil
		}
		return m.creatorEnterAgent(c.preflightName)
	case "backspace":
		if c.manualInput != "" {
			c.manualInput = c.manualInput[:len(c.manualInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			c.manualInput += msg.String()
		}
	}
	return m, nil
}

// creatorEnterGitHub switches to the GitHub browser and asks the daemon which
// identities it holds. Browsing is daemon-side so it works from any device.
func (m tuiModel) creatorEnterGitHub() (tea.Model, tea.Cmd) {
	c := m.creator
	c.step, c.ghLoading, c.err, c.ghHint = stepGitHub, true, "", ""
	c.ghPhase = ghPickIdentity
	c.ghIdentities, c.ghIdentCur = nil, 0
	c.ghRepos, c.ghRepoCur, c.ghIdentity = nil, 0, ""
	return m, c.ghIdentitiesCmd()
}

func (c *creatorModel) ghIdentitiesCmd() tea.Cmd {
	client := c.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ids, err := client.ListGitHubIdentities(ctx)
		return ghIdentitiesMsg{identities: ids, err: err}
	}
}

func (c *creatorModel) ghReposCmd(identity string) tea.Cmd {
	client := c.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		repos, capped, err := client.ListGitHubRepos(ctx, identity)
		return ghReposMsg{repos: repos, capped: capped, err: err}
	}
}

// onGhIdentities lands the identity list: a single one skips straight to its
// repos; none shows a hint pointing at `dejima auth push --github`.
func (m tuiModel) onGhIdentities(msg ghIdentitiesMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	c.ghLoading = false
	if msg.err != nil {
		c.err = msg.err.Error()
		// A failed lookup is not an answer. The preflight stays silent rather than
		// warning about a credential it could not see — see tui_create_preflight.go.
		if c.step == stepGitHubPreflight {
			return m.creatorEnterAgent(c.preflightName)
		}
		return m, nil
	}
	c.ghIdentities = msg.identities
	c.ghIdentitiesLoaded = true
	if c.step == stepGitHubPreflight {
		if creatorPreflightGitHub(c.resolution.Repo, c.ghIdentities, true) {
			return m, nil // stay here and show the warning
		}
		return m.creatorEnterAgent(c.preflightName)
	}
	switch len(c.ghIdentities) {
	case 0:
		// Adding a GitHub identity is owner-only (PUT /v1/credentials/github/{name}
		// is capOwner), so guide by role: an owner can connect one now; a teammate
		// (operator/viewer) has to ask the host owner.
		if c.callerRole == "operator" || c.callerRole == "viewer" {
			c.ghHint = "No GitHub identity on the server yet — and connecting one is the owner's call.\n" +
				"Ask the host owner to run `dejima auth push --github` (from a machine where\n" +
				"`gh` is logged in). Once they do, your GitHub repos show up here.\n\n" +
				"Meanwhile you can still start from a public git URL (back → “Enter a repo URL”)."
		} else {
			// [c] rather than a paragraph of homework. This screen used to tell an
			// owner to go run a command in another terminal and come back — which
			// is the settings pane's job description, and the settings pane has a
			// key for it. Reported from a fresh Windows install as "no github
			// connected (should be guided)": the guidance was there and it was
			// prose, so the operator read instructions instead of connecting.
			c.ghHint = "No GitHub identity on the server yet.\n\n" +
				"Press [c] to connect one now — it opens the guided sign-in in a new\n" +
				"window. Come back with [r] when it finishes and your repos appear.\n\n" +
				"(The daemon holds the identity, not this machine, so having `gh`\n" +
				"logged in locally is not enough on its own.)"
		}
		return m, nil
	case 1:
		return m.creatorSelectIdentity(c.ghIdentities[0]) // no point making them pick
	default:
		return m, nil
	}
}

func (c *creatorModel) onGhRepos(msg ghReposMsg) {
	c.ghLoading = false
	if msg.err != nil {
		c.err = msg.err.Error()
		return
	}
	c.ghRepos = msg.repos
	c.ghCapped = msg.capped
	if c.ghRepoCur >= len(c.ghRepos) {
		c.ghRepoCur = 0
	}
}

func (m tuiModel) creatorGitHubKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	if c.ghLoading {
		if msg.String() == "esc" {
			c.step, c.err = stepPick, ""
		}
		return m, nil
	}
	if c.ghHint != "" {
		switch msg.String() {
		case "c": // not "C": ESC [ C is the right arrow
			// --default: this fires only when NO identity resolves, so the one
			// being created is the one everything should follow. Leaving it
			// implicit is how a daemon ends up holding identities and no default,
			// which is its own week of confusion.
			// In the pane, not in a window — see the gate above for why. The
			// operator comes back to the creator afterwards; connecting an
			// identity is the prerequisite, not a step of this wizard.
			m.creator = nil
			return m.openGithubViewConnecting("")
		case "r": // not "R": ESC O R is F3
			// Re-ask the daemon rather than assuming the connect worked. If it did
			// not, the operator lands back on this same screen with the same key,
			// which is the correct place to be.
			c.ghLoading, c.ghHint, c.err = true, "", ""
			return m, c.ghIdentitiesCmd()
		case "esc", "ctrl+[", "enter", "q":
			c.step, c.err, c.ghHint = stepPick, "", ""
		}
		return m, nil
	}
	if c.ghPhase == ghPickIdentity {
		return m.creatorGitHubIdentityKey(msg)
	}
	return m.creatorGitHubRepoKey(msg)
}

func (m tuiModel) creatorGitHubIdentityKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		c.step, c.err = stepPick, "" // back to the repo picker
	case "up", "k":
		if c.ghIdentCur > 0 {
			c.ghIdentCur--
		}
	case "down", "j":
		if c.ghIdentCur < len(c.ghIdentities)-1 {
			c.ghIdentCur++
		}
	case "enter":
		if len(c.ghIdentities) == 0 {
			return m, nil
		}
		return m.creatorSelectIdentity(c.ghIdentities[c.ghIdentCur])
	}
	return m, nil
}

func (m tuiModel) creatorGitHubRepoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		if len(c.ghIdentities) > 1 {
			c.ghPhase, c.err = ghPickIdentity, "" // back to identity choice
		} else {
			c.step, c.err = stepPick, "" // only one identity: back to the repo picker
		}
	case "up", "k":
		if c.ghRepoCur > 0 {
			c.ghRepoCur--
		}
	case "down", "j":
		if c.ghRepoCur < len(c.ghRepos)-1 {
			c.ghRepoCur++
		}
	case "enter":
		if len(c.ghRepos) == 0 {
			return m, nil
		}
		return m.creatorSelectGitHub(c.ghRepos[c.ghRepoCur])
	}
	return m, nil
}

// creatorSelectIdentity records the chosen identity and loads its repos.
func (m tuiModel) creatorSelectIdentity(id githubid.Meta) (tea.Model, tea.Cmd) {
	c := m.creator
	c.ghIdentity = id.Name
	c.ghPhase = ghPickRepo
	c.ghRepos, c.ghRepoCur = nil, 0
	c.ghLoading, c.err = true, ""
	return m, c.ghReposCmd(id.Name)
}

// creatorSelectGitHub resolves a chosen GitHub repo (always a remote clone) and
// advances to agent selection; the chosen identity is already on the creator.
func (m tuiModel) creatorSelectGitHub(r githubid.Repo) (tea.Model, tea.Cmd) {
	c := m.creator
	res, err := reposrc.Resolve(r.URL, c.daemonLocal, false)
	if err != nil {
		c.err = err.Error()
		return m, nil
	}
	c.resolution, c.err = res, ""
	return m.creatorEnterAgent(project.DeriveNameFromRepo(r.URL))
}

func (m tuiModel) creatorSourceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		c.step = stepPick
	case "up", "k", "down", "j":
		c.sourceCursor = 1 - c.sourceCursor
	case "enter":
		forceLocal := c.sourceCursor == 1
		res, err := reposrc.Resolve(c.pendingPath, c.daemonLocal, forceLocal)
		if err != nil {
			c.err = err.Error()
			return m, nil
		}
		c.resolution = res
		return m.creatorEnterAgent(project.DeriveNameFromRepo(c.pendingPath))
	}
	return m, nil
}

func (m tuiModel) creatorEnterAgent(baseName string) (tea.Model, tea.Cmd) {
	c := m.creator
	c.step = stepAgent
	c.picker = newAgentPicker() // defaults to the first option (terminal) — always useful, swap to claude-code/codex as needed
	c.agents = nil
	c.pickingExtra = false
	c.nameInput = c.uniqueName(baseName)
	return m, nil
}

func (m tuiModel) creatorAgentKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch c.picker.handleKey(msg) {
	case pickerBack:
		// Backing out of an extra-agent pick returns to the roster (discarding
		// it); backing out of the primary pick returns to repo selection.
		if c.pickingExtra {
			c.pickingExtra = false
			c.step = stepAgents
		} else {
			c.step = stepName
		}
	case pickerDone:
		// Hold the spec and ask for its label before committing it, so an agent
		// can be named at creation time — previously the only way to name one was
		// to create the island, then rename it afterwards.
		c.pendingAgent = api.AgentSpecRequest{Type: c.picker.typ(), Cmd: c.picker.cmd()}
		c.agentNameIn = ""
		c.step = stepAgentName
	}
	return m, nil
}

// creatorAgentNameKey names the agent chosen at stepAgent. Blank keeps the
// agent's generated id, which is what the flow did before naming existed — so
// Enter straight through is unchanged for anyone who doesn't care.
func (m tuiModel) creatorAgentNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		c.agentNameIn = ""
		c.step = stepAgent
	case "enter":
		spec := c.pendingAgent
		spec.Label = strings.TrimSpace(c.agentNameIn)
		c.agents = append(c.agents, spec)
		c.pendingAgent = api.AgentSpecRequest{}
		c.agentNameIn = ""
		c.pickingExtra = false
		// Guided key: if this agent needs a provider key and none is set, collect
		// it now so the agent launches working instead of coming up broken.
		if provs := m.needsProviderKey(spec.Type); len(provs) > 0 {
			c.keyProviders, c.keyProvSel, c.keyInput = provs, 0, ""
			c.step = stepAgentKey
		} else {
			c.step = stepAgents
		}
	case "backspace":
		if c.agentNameIn != "" {
			c.agentNameIn = c.agentNameIn[:len(c.agentNameIn)-1]
		}
	default:
		if s := pastableInput(msg); s != "" {
			c.agentNameIn += s
		}
	}
	return m, nil
}

// needsProviderKey returns the providers a key-requiring agent could use when it
// has NO key configured, or nil when it's satisfied (or not key-requiring). The
// creator uses it to decide whether to guide a key entry before create.
func (m tuiModel) needsProviderKey(agentType string) []string {
	c := m.creator
	if c != nil && c.keySetOK != nil {
		// A provider key set earlier in this same flow counts.
		for _, p := range m.agentProviders[agentType] {
			if c.keySetOK[p] {
				return nil
			}
		}
	}
	if !m.agentKeyGap[agentType] {
		return nil
	}
	provs := m.agentProviders[agentType]
	if len(provs) == 0 {
		return nil // no advisory list — can't guide a specific provider
	}
	return provs
}

// creatorProviderKeyKey drives the guided key step: pick a provider, paste the
// key (masked), Enter to store it, then continue to the roster.
func (m tuiModel) creatorProviderKeyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	if c.keyBusy {
		return m, nil // waiting on the store; ignore keys
	}
	switch msg.String() {
	case "esc", "ctrl+[":
		// Skip: proceed without a key. The agent will need `v` later — the picker
		// already warns — but we don't force it.
		c.step = stepAgents
	case "up":
		// Arrow keys only for provider nav — j/k are valid key characters and must
		// be typed, not swallowed as vim motions.
		if c.keyProvSel > 0 {
			c.keyProvSel--
		}
	case "down":
		if c.keyProvSel < len(c.keyProviders)-1 {
			c.keyProvSel++
		}
	case "enter":
		if strings.TrimSpace(c.keyInput) == "" {
			return m, nil // nothing to store yet
		}
		provider := c.keyProviders[c.keyProvSel]
		c.keyBusy = true
		return m, m.creatorSetKeyCmd(provider, c.keyInput)
	case "backspace":
		if c.keyInput != "" {
			c.keyInput = c.keyInput[:len(c.keyInput)-1]
		}
	default:
		// An API key, so the silent half matters more than the visible one: a
		// non-ASCII character in a pasted key was dropped without a word.
		if s := pastableInput(msg); s != "" {
			c.keyInput += s
		}
	}
	return m, nil
}

// creatorSetKeyCmd stores a provider key during creation (provider-level, so it
// applies before the agent exists), reporting back via creatorKeySetMsg.
func (m tuiModel) creatorSetKeyCmd(provider, key string) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := cl.PutProviderCredential(ctx, provider, api.PutProviderCredentialRequest{APIKey: key})
		return creatorKeySetMsg{provider: provider, err: err}
	}
}

type creatorKeySetMsg struct {
	provider string
	err      error
}

// creatorAgentsKey drives the roster: review the seeded agents, add another, drop
// the last extra, or continue to naming.
func (m tuiModel) creatorAgentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "a":
		c.pickingExtra = true
		c.picker = newAgentPicker()
		c.step = stepAgent
	case "d", "backspace":
		if len(c.agents) > 1 { // never drop the primary
			c.agents = c.agents[:len(c.agents)-1]
		}
	case "enter":
		c.creating, c.step, c.err = true, stepCreate, ""
		return m, c.createCmd()
	case "esc", "ctrl+[":
		// Re-pick from scratch: clear the roster and choose the primary again.
		c.agents = nil
		c.pickingExtra = false
		c.picker = newAgentPicker()
		c.step = stepAgent
	}
	return m, nil
}

func (m tuiModel) creatorNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		c.step = stepPick
	case "enter":
		if strings.TrimSpace(c.nameInput) == "" {
			return m, nil
		}
		if err := project.ValidateName(c.nameInput); err != nil {
			c.err = err.Error()
			return m, nil
		}
		c.err = ""
		c.step = stepAgent
	case "backspace":
		if c.nameInput != "" {
			c.nameInput = c.nameInput[:len(c.nameInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			c.nameInput += msg.String()
		}
	}
	return m, nil
}

// uniqueName derives a valid, non-colliding island name from a base.
func (c *creatorModel) uniqueName(base string) string {
	name := project.DeriveNameFromRepo(base)
	taken := map[string]bool{}
	for _, isl := range c.existing {
		taken[isl.Name] = true
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

// islandsForRepo counts existing islands whose repo matches this origin.
func (c *creatorModel) islandsForRepo(origin string) int {
	if origin == "" {
		return 0
	}
	n := 0
	for _, isl := range c.existing {
		if isl.Repo == origin {
			n++
		}
	}
	return n
}

// --- view -----------------------------------------------------------------

func (c *creatorModel) view(width int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("New island"))
	b.WriteString("\n\n")

	switch c.step {
	case stepRoot:
		c.viewRoot(&b)
	case stepPick:
		c.viewPick(&b)
	case stepManual:
		c.viewManual(&b)
	case stepGitHub:
		c.viewGitHub(&b)
	case stepSource:
		c.viewSource(&b)
	case stepAgent:
		c.viewAgent(&b)
	case stepAgentName:
		c.viewAgentName(&b)
	case stepAgentKey:
		c.viewAgentKey(&b)
	case stepAgents:
		c.viewAgents(&b)
	case stepName:
		c.viewName(&b)
	case stepCreate:
		b.WriteString(styleAccent.Render("provisioning " + c.nameInput + "…"))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render(c.resolution.Note))
		b.WriteString("\n")
		if c.imageMissing {
			b.WriteString(styleMuted.Render("building the base image (first time, a few minutes), then cloning the repo\nand starting the agent; it opens when ready."))
		} else {
			b.WriteString(styleMuted.Render("cloning the repo and starting the agent; it opens when ready."))
		}
	case stepFromDir:
		c.viewFromDir(&b)
	case stepGitHubPreflight:
		if c.ghLoading {
			b.WriteString(styleMuted.Render("checking your GitHub identities…"))
			return b.String()
		}
		b.WriteString(renderGitHubPreflight(c.resolution.Repo))
		return b.String()
	case stepGitHubGate:
		b.WriteString(styleWaiting.Render("🔒 " + c.gateRepo + " needs your GitHub to clone"))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("  It's private (or not anonymously reachable) and no GitHub identity is set."))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("  Connect once and every island you create can clone your private repos."))
		b.WriteString("\n\n")
		// Not necessarily "guided": on a self-hosted daemon (no OAuth app) the
		// command falls back to the gh CLI, and failing that prompts for a token.
		// Promising a guided sign-in that can't happen is what sent operators to
		// a window that exited 1.
		b.WriteString("  " + styleAccent.Render("[c]") + " Connect your GitHub\n")
		b.WriteString("  " + styleAccent.Render("[Enter]") + " I've connected — retry the clone\n")
		b.WriteString("  " + styleMuted.Render("[f] Create anyway, authenticate later   ·   [esc] Cancel"))
	}

	if c.err != "" {
		b.WriteString("\n\n")
		b.WriteString(styleErrored.Render("✗ " + c.err))
	}
	return b.String()
}

func (c *creatorModel) viewRoot(b *strings.Builder) {
	b.WriteString(styleMuted.Render("What should this island start from?"))
	b.WriteString("\n\n")
	if c.rootTyping {
		b.WriteString("directory: " + styleAccent.Render(c.rootInput+"_"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] scan it   [esc] back"))
		return
	}
	for i, choice := range c.rootChoices {
		c.writeChoice(b, i == c.rootCursor, choice)
	}
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [/] type a URL or path   [esc] cancel"))
}

func (c *creatorModel) viewPick(b *strings.Builder) {
	// Remote sources lead. Cloning from GitHub is the common case — a local repo
	// is the exception, and burying the GitHub row under a scan of whatever
	// happens to be in one directory made the primary path the least visible.
	// Two labelled sections, so it reads as a choice between kinds of source
	// rather than one long undifferentiated list.
	// The question this list answers is "what goes in /workspace?", and a repo is
	// only ONE answer to it. Starting empty leads because it is the answer that is
	// not a repo at all, and burying it under two repo headings framed the whole
	// prompt as "which repo?".
	c.writeHeader(b, "Start empty")
	c.writeChoice(b, c.repoCursor == pickRowNoRepo, "␀  No repo — /workspace starts empty")

	b.WriteString("\n")
	c.writeHeader(b, "Clone from GitHub")
	c.writeChoice(b, c.repoCursor == pickRowGitHub, "⬇  Browse my GitHub repos…")
	c.writeChoice(b, c.repoCursor == pickRowManual, "✎  Enter a repo URL or path…")

	b.WriteString("\n")
	c.writeHeader(b, "Use a local folder")
	c.writeChoice(b, c.repoCursor == pickRowFromDir, "📁  A folder that isn't a repo — just files…")

	b.WriteString("\n")
	c.writeHeader(b, "Use a local repo — "+tildeify(c.root))
	switch {
	case c.scanning:
		b.WriteString(styleAccent.Render("  ⏳ scanning…") + "\n")
	case len(c.repos) == 0:
		b.WriteString(styleMuted.Render("  no git repos found here") + "\n")
	default:
		for i, repo := range c.repos {
			line := fmt.Sprintf("%-22s %s", truncate(repo.Name, 22), c.repoMeta(repo))
			c.writeChoice(b, c.repoCursor == pickRowFirstRepo+i, line)
		}
	}
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [/] type a URL/path   [esc] cancel"))
}

func (c *creatorModel) viewGitHub(b *strings.Builder) {
	if c.ghLoading {
		b.WriteString(styleAccent.Render("⏳ loading from the daemon…"))
		return
	}
	if c.ghHint != "" {
		b.WriteString(styleMuted.Render(c.ghHint))
		b.WriteString("\n\n" + styleMuted.Render("[⏎/esc] back"))
		return
	}
	if c.ghPhase == ghPickIdentity {
		b.WriteString(styleMuted.Render("Which GitHub identity?"))
		b.WriteString("\n\n")
		for i, id := range c.ghIdentities {
			meta := id.Login + "@" + id.Host
			if id.Default {
				meta += " · default"
			}
			line := fmt.Sprintf("%-14s %s", truncate(id.Name, 14), styleMuted.Render(meta))
			c.writeChoice(b, i == c.ghIdentCur, line)
		}
		b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [esc] back"))
		return
	}
	// Repo list for the chosen identity.
	b.WriteString(styleMuted.Render("Repos for ") + styleAccent.Render(c.ghIdentity))
	b.WriteString("\n\n")
	if len(c.ghRepos) == 0 {
		b.WriteString(styleMuted.Render("no repositories found.\n\n[esc] back"))
		return
	}
	// Window the list so a large account doesn't overflow the pane.
	const window = 12
	start := 0
	if c.ghRepoCur >= window {
		start = c.ghRepoCur - window + 1
	}
	end := start + window
	if end > len(c.ghRepos) {
		end = len(c.ghRepos)
	}
	for i := start; i < end; i++ {
		r := c.ghRepos[i]
		detail := []string{}
		if r.Private {
			detail = append(detail, "private")
		}
		if r.Description != "" {
			detail = append(detail, r.Description)
		}
		line := fmt.Sprintf("%-30s %s", truncate(r.NameWithOwner, 30),
			styleMuted.Render(truncate(strings.Join(detail, " · "), 44)))
		c.writeChoice(b, i == c.ghRepoCur, line)
	}
	if end < len(c.ghRepos) || start > 0 {
		total := fmt.Sprintf("%d", len(c.ghRepos))
		if c.ghCapped {
			total = fmt.Sprintf("first %d (more exist — refine on GitHub)", len(c.ghRepos))
		}
		b.WriteString(styleMuted.Render(fmt.Sprintf("  … %d–%d of %s\n", start+1, end, total)))
	} else if c.ghCapped {
		b.WriteString(styleMuted.Render(fmt.Sprintf("  showing the first %d — more exist (refine on GitHub)\n", len(c.ghRepos))))
	}
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [esc] back"))
}

// repoMeta renders the dimmed right-hand detail for a repo row: remote, working
// state (for the highlighted row, once status has loaded), and existing islands.
func (c *creatorModel) repoMeta(repo reposrc.Repo) string {
	parts := []string{}
	if repo.Origin != "" {
		parts = append(parts, shortRemote(repo.Origin))
	} else {
		parts = append(parts, "no remote")
	}
	if st, ok := c.statusCache[repo.Path]; ok {
		if st.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("%d ahead", st.Ahead))
		}
		if st.Dirty {
			parts = append(parts, "dirty")
		}
	}
	if n := c.islandsForRepo(repo.Origin); n > 0 {
		parts = append(parts, fmt.Sprintf("%d island(s)", n))
	}
	return styleMuted.Render(strings.Join(parts, " · "))
}

func (c *creatorModel) viewManual(b *strings.Builder) {
	b.WriteString(styleMuted.Render("Enter a git URL (git@…/https://…) or a local path."))
	b.WriteString("\n\n")
	b.WriteString("repo: " + styleAccent.Render(c.manualInput+"_"))
	b.WriteString("\n\n" + styleMuted.Render("[⏎] continue   [esc] back"))
}

func (c *creatorModel) viewSource(b *strings.Builder) {
	b.WriteString(styleMuted.Render(fmt.Sprintf(
		"%q has %d commit(s) not on its remote. Clone from:", project.DeriveNameFromRepo(c.pendingPath), c.pendingAhead)))
	b.WriteString("\n\n")
	c.writeChoice(b, c.sourceCursor == 0,
		"origin ("+shortRemote(c.pendingOrigin)+") — canonical; omits the unpushed commits")
	c.writeChoice(b, c.sourceCursor == 1,
		"this local copy — includes unpushed work (origin still set to the remote)")
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [esc] back"))
}

func (c *creatorModel) viewAgent(b *strings.Builder) {
	b.WriteString(styleMuted.Render(c.resolution.Note))
	b.WriteString("\n\n")
	c.picker.view(b, "Agent", c.keyGap)
}

// viewAgentName asks for the agent's display name.
func (c *creatorModel) viewAgentName(b *strings.Builder) {
	b.WriteString(styleMuted.Render(c.resolution.Note))
	b.WriteString("\n\n")
	b.WriteString(styleMuted.Render("Name this " + c.pendingAgent.Type + " agent (e.g. \"frontend\") — or leave blank to use its id."))
	b.WriteString("\n\n")
	b.WriteString("agent name: " + styleAccent.Render(c.agentNameIn+"_"))
	b.WriteString("\n\n" + styleMuted.Render("[⏎] continue   [esc] pick a different agent"))
}

// viewAgentKey renders the guided provider-key step: a key-requiring agent has
// no key, so collect one now (provider-level) rather than let it launch broken.
func (c *creatorModel) viewAgentKey(b *strings.Builder) {
	b.WriteString(styleWaiting.Render(c.pendingAgent.Type + " needs a provider key to work"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("Set it now and the agent launches ready. (You can skip and set it later with `v`.)"))
	b.WriteString("\n\n")

	b.WriteString(styleMuted.Render("provider:"))
	b.WriteString("\n")
	for i, p := range c.keyProviders {
		if i == c.keyProvSel {
			b.WriteString("  " + styleSelected.Render("▶ "+p) + "\n")
		} else {
			b.WriteString("    " + p + "\n")
		}
	}
	b.WriteString("\n")
	// Mask the key — length only, never the characters.
	b.WriteString("key: " + styleAccent.Render(strings.Repeat("•", len(c.keyInput))+"▏"))
	b.WriteString("\n\n")
	if c.keyBusy {
		b.WriteString(styleAccent.Render("saving…"))
		return
	}
	if c.err != "" {
		b.WriteString(styleErrored.Render("✗ "+c.err) + "\n\n")
	}
	b.WriteString("  " + styleSelected.Render(" Save & continue (⏎) ") + "    " + styleMuted.Render(" Skip (esc) "))
	b.WriteString("\n" + styleMuted.Render("[↑/↓] provider · type the key (hidden)"))
}

// viewAgents renders the seeded-agent roster: the primary plus any extras, with
// keys to add another, drop the last, or continue.
func (c *creatorModel) viewAgents(b *strings.Builder) {
	b.WriteString(styleMuted.Render(c.resolution.Note))
	b.WriteString("\n\n")
	b.WriteString(styleMuted.Render("Agents to seed (or none — you can shell in and add agents later):"))
	b.WriteString("\n\n")
	for i, a := range c.agents {
		role := fmt.Sprintf("agent %d", i+1)
		name := a.Label
		if name == "" {
			name = styleMuted.Render("(auto id)")
		}
		line := fmt.Sprintf("%-9s %-14s %s", role, name, a.Type)
		if a.Cmd != "" {
			line += "  — " + a.Cmd
		}
		c.writeChoice(b, false, line)
	}
	b.WriteString("\n" + styleMuted.Render("[a] add another   [d] remove last   [⏎] create & connect   [esc] start over"))
}

func (c *creatorModel) viewName(b *strings.Builder) {
	summary := "no agent"
	if len(c.agents) > 0 {
		summary = c.agents[0].Type
		if len(c.agents) > 1 {
			summary = fmt.Sprintf("%d agents (%s + %d more)", len(c.agents), c.agents[0].Type, len(c.agents)-1)
		}
	}
	b.WriteString(styleMuted.Render(fmt.Sprintf("%s · %s", summary, c.resolution.Note)))
	b.WriteString("\n\n")
	b.WriteString("island name: " + styleAccent.Render(c.nameInput+"_"))
	if c.noRepo && strings.TrimSpace(c.nameInput) == "" {
		// The field starts empty here by design, so say why rather than leaving a
		// bare cursor that looks like the step forgot to prefill.
		b.WriteString("\n" + styleMuted.Render("  there's no repo to name this after — type one"))
	}
	if c.imageMissing {
		b.WriteString("\n\n" + styleWaiting.Render("ℹ first island — this also builds the base image (one-time, a few minutes)."))
	}
	b.WriteString("\n\n" + styleMuted.Render("[⏎] next: choose an agent   [esc] back"))
}

// Fixed row indices for the repo picker. The remote-source actions occupy the
// first rows, then "start empty", then the discovered local repos. Named because
// the cursor arithmetic is shared between the view, the key handler and the enter
// action — index drift between those three is exactly the bug this prevents.
//
// pickRowNoRepo is deliberately NOT row 0. repoCursor starts at 0, so leading with
// it would make `n` then ⏎ create an empty island for everyone — silently changing
// the default action of the most-used flow. Repo-less is a rare, deliberate choice;
// it has to be visible, not default.
const (
	pickRowNoRepo = 0
	// Rows on the pre-scan root screen. Named because they are referenced from
	// three places and an off-by-one silently sends the operator somewhere else.
	rootRowClone = 0 // a repo from elsewhere: GitHub browse or a git URL
	rootRowLocal = 1 // a git repo already on this machine
	rootRowEmpty = 2 // nothing; files arrive later

	pickRowGitHub    = 1
	pickRowManual    = 2
	pickRowFromDir   = 3
	pickRowFirstRepo = 4
)

// writeHeader renders a non-selectable section label. It deliberately consumes
// no cursor index — the list widget maps rows to indices 1:1, so a header has to
// be printed outside that mapping.
func (c *creatorModel) writeHeader(b *strings.Builder, text string) {
	b.WriteString(styleMuted.Render("  " + text))
	b.WriteString("\n")
}

// rootSourceChoices builds the three rows of the first create screen.
//
// It exists as a function so the TEST can assert on the rows that actually
// ship. The earlier test declared its own copy of these strings and checked
// that copy, which meant the row constants could drift from the real list
// without failing anything — and an off-by-one there runs a different action
// than the highlighted line, with nothing looking wrong.
//
// The leading glyphs are single-width BMP characters on purpose. Emoji are
// double-width in most terminals and inconsistently so across them, which
// would misalign the muted descriptions on exactly the machines we cannot see.
//
// Order is deliberate and is pinned by tests: cloning leads because it is the
// common case AND the only one of the three that cannot be reached later from
// inside the others. Starting empty is last because it is the cheapest to
// change your mind about.
func rootSourceChoices(machine string) []string {
	row := func(icon, label, desc string) string {
		return fmt.Sprintf("%-24s%s", icon+"  "+label, styleMuted.Render(desc))
	}
	return []string{
		row("\u21e3", "Clone a repo", "browse GitHub, or paste a git URL"),
		row("\u2302", "Use a local repo", "a git repo already on "+machine+"'s machine"),
		row("\u25cc", "Start empty", "no repo — add files later"),
	}
}

func (c *creatorModel) writeChoice(b *strings.Builder, selected bool, text string) {
	if selected {
		b.WriteString(styleSelected.Render("▶ " + text))
	} else {
		b.WriteString("  " + text)
	}
	b.WriteString("\n")
}

// shortRemote trims a remote URL to a readable host/owner/repo form.
func shortRemote(url string) string {
	s := strings.TrimSuffix(url, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if at := strings.Index(s, "@"); at >= 0 {
			s = s[at+1:]
		}
	} else if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:] // git@github.com:owner/repo → github.com:owner/repo
	}
	return s
}

// creatorFromDirKey drives the folder-source step: type a host path, [tab]
// toggles `git init`, ⏎ accepts.
func (m tuiModel) creatorFromDirKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc", "ctrl+[":
		c.step, c.err = stepPick, ""
	case "tab":
		c.fromDirGit = !c.fromDirGit
	case "enter":
		in := strings.TrimSpace(c.fromDirInput)
		if in == "" {
			c.err = "type the folder to seed /workspace from"
			return m, nil
		}
		abs, err := filepath.Abs(expandTilde(in))
		if err != nil {
			c.err = err.Error()
			return m, nil
		}
		info, err := os.Stat(abs)
		if err != nil {
			c.err = "can't read " + abs + ": " + err.Error()
			return m, nil
		}
		if !info.IsDir() {
			c.err = abs + " is a file, not a folder"
			return m, nil
		}
		c.fromDir, c.err = abs, ""
		// Same shape as the repo-less branch: nothing for the resolver to resolve,
		// and a name is required because a folder's basename ("src", "notes") makes
		// an unpredictable, colliding island name.
		c.resolution = reposrc.Resolution{Note: "seeding /workspace from " + tildeify(abs) + " — brokered, one Ledger entry per file"}
		mm, cmd := m.creatorEnterAgent("")
		c.nameInput = ""
		c.step = stepName
		return mm, cmd
	case "backspace":
		if c.fromDirInput != "" {
			c.fromDirInput = c.fromDirInput[:len(c.fromDirInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			c.fromDirInput += msg.String()
		}
	}
	return m, nil
}

func (c *creatorModel) viewFromDir(b *strings.Builder) {
	b.WriteString(styleMuted.Render("A folder of work that isn't a repo yet — it's copied in through Port,") + "\n")
	b.WriteString(styleMuted.Render("one Ledger entry per file. Symlinks are never followed.") + "\n\n")
	b.WriteString("folder: " + styleAccent.Render(c.fromDirInput+"_") + "\n\n")
	git := "no — /workspace holds the files, with no repo"
	if c.fromDirGit {
		// The cost is stated here rather than at the toggle, because this is the
		// moment the choice is being made and a repo with no remote is a state
		// whose surface implies something untrue.
		git = "yes — WARNING: a repo with no remote. Commits go nowhere pushable"
	}
	b.WriteString(styleMuted.Render("git init: ") + git + "\n")
	if c.err != "" {
		b.WriteString("\n" + styleErrored.Render(c.err) + "\n")
	}
	b.WriteString("\n" + styleMuted.Render("[⏎] continue   [tab] git init on/off   [esc] back"))
}
