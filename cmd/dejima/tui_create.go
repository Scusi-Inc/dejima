package main

import (
	"context"
	"fmt"
	"io"
	"os"
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
	stepRoot   creatorStep = iota // first-load: choose a directory to scan
	stepPick                      // pick a discovered repo (or switch to manual)
	stepManual                    // type a URL or path
	stepGitHub                    // browse a daemon GitHub identity's repos
	stepSource                    // diverged local repo: clone origin vs local copy
	stepAgent                     // choose the agent
	stepName                      // confirm/edit the island name
	stepCreate                    // provisioning in flight
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
	ghIdentity   string // chosen identity name → CreateIslandRequest.GitHubIdentity
	ghIdentCur   int
	ghRepos      []githubid.Repo
	ghRepoCur    int
	ghLoading    bool
	ghHint       string // shown when the daemon has no identities

	// source-divergence prompt
	pendingPath   string
	pendingOrigin string
	pendingAhead  int
	sourceCursor  int

	// resolved selection
	resolution reposrc.Resolution
	picker     agentPicker // agent type (and headless command) chooser
	nameInput  string
	creating   bool
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
	err  error
}
type ghIdentitiesMsg struct {
	identities []githubid.Meta
	err        error
}
type ghReposMsg struct {
	repos []githubid.Repo
	err   error
}

// --- entry / commands -----------------------------------------------------

// openCreator initializes the new-island flow: straight to the picker if a
// scan root is already configured, otherwise to the first-load root prompt.
func (m tuiModel) openCreator() (tea.Model, tea.Cmd) {
	cfg, _ := clientcfg.Load()
	c := &creatorModel{
		client:      m.client,
		daemonLocal: os.Getenv("DEJIMA_HOST") == "",
		existing:    m.islands,
		statusCache: map[string]reposrc.Status{},
	}
	m.creator = c
	if cfg.RepoRoot == "" {
		pwd, _ := os.Getwd()
		c.step = stepRoot
		c.rootChoices = []string{
			"Scan this directory (" + tildeify(pwd) + ")",
			"Choose another directory…",
			"Skip — enter a repo URL or path manually",
		}
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

func (c *creatorModel) createCmd() tea.Cmd {
	client := c.client
	req := api.CreateIslandRequest{
		Name:           c.nameInput,
		Repo:           c.resolution.Repo,
		SeedPath:       c.resolution.SeedPath,
		Agent:          c.picker.typ(),
		Cmd:            c.picker.cmd(), // headless only; empty for interactive agents
		GitHubIdentity: c.ghIdentity,   // "" unless sourced via the GitHub browser
	}
	return func() tea.Msg {
		// Auto-build the island image when the daemon doesn't have it yet
		// (fresh host) — first island creation Just Works, it just takes the
		// build's few extra minutes.
		if o, err := client.Overview(context.Background()); err == nil && !o.IslandImagePresent {
			bctx, bcancel := context.WithTimeout(context.Background(), 30*time.Minute)
			err := client.BuildImage(bctx, io.Discard)
			bcancel()
			if err != nil {
				return islandCreatedMsg{err: fmt.Errorf("build island image: %w", err)}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		info, err := client.CreateIsland(ctx, req)
		if err != nil {
			return islandCreatedMsg{err: err}
		}
		return islandCreatedMsg{name: info.Name}
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
	if c.repoCursor >= len(c.repos) {
		c.repoCursor = 0
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
	case stepName:
		return m.creatorNameKey(msg)
	}
	return m, nil // stepCreate: ignore input while provisioning
}

func (m tuiModel) creatorRootKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	if c.rootTyping {
		switch msg.String() {
		case "esc":
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
	case "esc", "q":
		m.creator = nil
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
		case 0:
			pwd, _ := os.Getwd()
			_ = clientcfg.Save(clientcfg.Config{RepoRoot: pwd})
			c.root, c.step, c.scanning = pwd, stepPick, true
			return m, discoverCmd(pwd)
		case 1:
			c.rootTyping, c.rootInput = true, ""
		case 2:
			c.step = stepManual
		}
	}
	return m, nil
}

func (m tuiModel) creatorPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	// Two action rows sit below the discovered repos: enter-a-URL and browse-GitHub.
	lastRow := len(c.repos) + 1
	switch msg.String() {
	case "esc", "q":
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
	case len(c.repos): // ✎ enter a URL or path
		c.step, c.manualInput, c.err = stepManual, "", ""
		return m, nil
	case len(c.repos) + 1: // ⬇ browse GitHub
		return m.creatorEnterGitHub()
	default:
		if len(c.repos) == 0 {
			return m, nil
		}
		return m.creatorSelectRepo(c.repos[c.repoCursor])
	}
}

// ensureStatus lazily fetches working-tree status for the highlighted repo. No-op
// when the cursor is on one of the trailing action rows.
func (c *creatorModel) ensureStatus() tea.Cmd {
	if c.repoCursor >= len(c.repos) {
		return nil
	}
	p := c.repos[c.repoCursor].Path
	if _, ok := c.statusCache[p]; ok {
		return nil
	}
	return repoStatusCmd(p)
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
	case "esc":
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
		return m.creatorEnterAgent(project.DeriveNameFromRepo(in))
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
		repos, err := client.ListGitHubRepos(ctx, identity)
		return ghReposMsg{repos: repos, err: err}
	}
}

// onGhIdentities lands the identity list: a single one skips straight to its
// repos; none shows a hint pointing at `dejima auth push --github`.
func (m tuiModel) onGhIdentities(msg ghIdentitiesMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	c.ghLoading = false
	if msg.err != nil {
		c.err = msg.err.Error()
		return m, nil
	}
	c.ghIdentities = msg.identities
	switch len(c.ghIdentities) {
	case 0:
		c.ghHint = "No GitHub identities on the daemon yet.\n" +
			"Add one with `dejima auth push --github` (from a machine with gh),\n" +
			"or run `gh auth login` on the daemon host — then come back."
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
	if c.ghHint != "" { // no identities: any key but movement returns to the picker
		if s := msg.String(); s == "esc" || s == "enter" || s == "q" {
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
	case "esc":
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
	case "esc":
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
	case "esc":
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
	c.nameInput = c.uniqueName(baseName)
	return m, nil
}

func (m tuiModel) creatorAgentKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch c.picker.handleKey(msg) {
	case pickerBack:
		c.step = stepPick
	case pickerDone:
		c.step = stepName
	}
	return m, nil
}

func (m tuiModel) creatorNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "esc":
		c.step = stepAgent
	case "enter":
		if strings.TrimSpace(c.nameInput) == "" {
			return m, nil
		}
		if err := project.ValidateName(c.nameInput); err != nil {
			c.err = err.Error()
			return m, nil
		}
		c.creating, c.step, c.err = true, stepCreate, ""
		return m, c.createCmd()
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
	case stepName:
		c.viewName(&b)
	case stepCreate:
		b.WriteString(styleAccent.Render("provisioning " + c.nameInput + "…"))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render(c.resolution.Note))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("cloning the repo and starting the agent; you'll attach automatically."))
	}

	if c.err != "" {
		b.WriteString("\n\n")
		b.WriteString(styleErrored.Render("✗ " + c.err))
	}
	return b.String()
}

func (c *creatorModel) viewRoot(b *strings.Builder) {
	b.WriteString(styleMuted.Render("Where are your repos? The picker scans this directory for git repos."))
	b.WriteString("\n\n")
	if c.rootTyping {
		b.WriteString("directory: " + styleAccent.Render(c.rootInput+"_"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] scan it   [esc] back"))
		return
	}
	for i, choice := range c.rootChoices {
		c.writeChoice(b, i == c.rootCursor, choice)
	}
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [esc] cancel"))
}

func (c *creatorModel) viewPick(b *strings.Builder) {
	b.WriteString(styleMuted.Render("Repos in " + tildeify(c.root)))
	b.WriteString("\n\n")
	if c.scanning {
		b.WriteString(styleMuted.Render("scanning…"))
		return
	}
	if len(c.repos) == 0 {
		b.WriteString(styleMuted.Render("no git repos found here — pull from a URL or GitHub instead."))
		b.WriteString("\n\n")
	}
	for i, repo := range c.repos {
		line := fmt.Sprintf("%-22s %s", truncate(repo.Name, 22), c.repoMeta(repo))
		c.writeChoice(b, i == c.repoCursor, line)
	}
	// Always-present sources below the discovered repos.
	c.writeChoice(b, c.repoCursor == len(c.repos), "✎  Enter a repo URL or path…")
	c.writeChoice(b, c.repoCursor == len(c.repos)+1, "⬇  Browse my GitHub repos…")
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [/] type a URL/path   [esc] cancel"))
}

func (c *creatorModel) viewGitHub(b *strings.Builder) {
	if c.ghLoading {
		b.WriteString(styleMuted.Render("loading from the daemon…"))
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
		b.WriteString(styleMuted.Render(fmt.Sprintf("  … %d–%d of %d\n", start+1, end, len(c.ghRepos))))
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
	c.picker.view(b, "Agent")
}

func (c *creatorModel) viewName(b *strings.Builder) {
	b.WriteString(styleMuted.Render(fmt.Sprintf("Agent %s · %s", c.picker.typ(), c.resolution.Note)))
	b.WriteString("\n\n")
	b.WriteString("island name: " + styleAccent.Render(c.nameInput+"_"))
	b.WriteString("\n\n" + styleMuted.Render("[⏎] create & connect   [esc] back"))
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
