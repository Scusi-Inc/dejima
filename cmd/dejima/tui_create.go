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
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/reposrc"
)

type creatorStep int

const (
	stepRoot   creatorStep = iota // first-load: choose a directory to scan
	stepPick                      // pick a discovered repo (or switch to manual)
	stepManual                    // type a URL or path
	stepSource                    // diverged local repo: clone origin vs local copy
	stepAgent                     // choose an agent (primary, then any extras)
	stepAgents                    // roster: review seeded agents, add more, or continue
	stepName                      // confirm/edit the island name
	stepCreate                    // provisioning in flight
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

	// source-divergence prompt
	pendingPath   string
	pendingOrigin string
	pendingAhead  int
	sourceCursor  int

	// resolved selection
	resolution   reposrc.Resolution
	picker       agentPicker            // agent type (and headless command) chooser
	agents       []api.AgentSpecRequest // seeded agents; element 0 is the primary
	pickingExtra bool                   // true while the picker is adding a non-primary agent
	nameInput    string
	creating     bool
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

// buildRequest assembles the create request from the resolved selection and the
// seeded agent roster. A single agent uses the scalar Agent/Cmd fields (request
// identical to the pre-multi-seed flow); extras populate Agents (element 0 is
// the primary, mirrored into the scalar fields too).
func (c *creatorModel) buildRequest() api.CreateIslandRequest {
	req := api.CreateIslandRequest{
		Name:     c.nameInput,
		Repo:     c.resolution.Repo,
		SeedPath: c.resolution.SeedPath,
		Agent:    c.agents[0].Type, // primary (scalar back-compat path)
		Cmd:      c.agents[0].Cmd,  // headless only; empty for interactive agents
	}
	if len(c.agents) > 1 {
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
	case stepSource:
		return m.creatorSourceKey(msg)
	case stepAgent:
		return m.creatorAgentKey(msg)
	case stepAgents:
		return m.creatorAgentsKey(msg)
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
		if c.repoCursor < len(c.repos)-1 {
			c.repoCursor++
		}
		return m, c.ensureStatus()
	case "enter":
		if len(c.repos) == 0 {
			return m, nil
		}
		return m.creatorSelectRepo(c.repos[c.repoCursor])
	}
	return m, nil
}

// ensureStatus lazily fetches working-tree status for the highlighted repo.
func (c *creatorModel) ensureStatus() tea.Cmd {
	if len(c.repos) == 0 {
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
			c.step = stepPick
		}
	case pickerDone:
		c.agents = append(c.agents, api.AgentSpecRequest{Type: c.picker.typ(), Cmd: c.picker.cmd()})
		c.pickingExtra = false
		c.step = stepAgents
	}
	return m, nil
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
		c.step = stepName
	case "esc":
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
	case "esc":
		c.step = stepAgents
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
	case stepSource:
		c.viewSource(&b)
	case stepAgent:
		c.viewAgent(&b)
	case stepAgents:
		c.viewAgents(&b)
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
		b.WriteString(styleMuted.Render("no git repos found here.\n\nPress [/] to enter a repo URL or path manually, or [esc] to cancel."))
		return
	}
	for i, repo := range c.repos {
		line := fmt.Sprintf("%-22s %s", truncate(repo.Name, 22), c.repoMeta(repo))
		c.writeChoice(b, i == c.repoCursor, line)
	}
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] select   [/] type a URL/path   [esc] cancel"))
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

// viewAgents renders the seeded-agent roster: the primary plus any extras, with
// keys to add another, drop the last, or continue.
func (c *creatorModel) viewAgents(b *strings.Builder) {
	b.WriteString(styleMuted.Render(c.resolution.Note))
	b.WriteString("\n\n")
	b.WriteString(styleMuted.Render("Agents to seed (the first is primary — it backs `dejima connect`):"))
	b.WriteString("\n\n")
	for i, a := range c.agents {
		role := "primary"
		if i > 0 {
			role = fmt.Sprintf("agent %d", i+1)
		}
		line := fmt.Sprintf("%-9s %s", role, a.Type)
		if a.Cmd != "" {
			line += "  — " + a.Cmd
		}
		c.writeChoice(b, false, line)
	}
	b.WriteString("\n" + styleMuted.Render("[a] add another   [d] remove last   [⏎] continue   [esc] start over"))
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
