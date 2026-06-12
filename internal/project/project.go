package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/aoos/dejima/internal/paths"
)

// agentTypeHeadless mirrors api.AgentHeadless. It's duplicated here to avoid an
// import cycle (the api package imports project, not vice-versa). The handler
// registry (internal/handlers) centralizes agent-type knowledge in a later phase.
const agentTypeHeadless = "headless"

// State is the desired state of an island.
type State string

const (
	StateRunning    State = "running"
	StateHibernated State = "hibernated"
)

// Island roles. Empty (RoleProject) is the default work/coding island. RoleHome
// marks a persistent "Home Island" that hosts an always-on assistant
// orchestrator (the brain), which reaches host content only through the Port
// and spawns work islands via the API. See docs/port-island-spec.md §3.2.
const (
	RoleProject = ""
	RoleHome    = "home"
)

// Resources captures the docker resource caps applied to the container.
// All fields optional; zero/empty means unlimited.
type Resources struct {
	Memory string `toml:"memory,omitempty"` // e.g. "4G"
	CPUs   string `toml:"cpus,omitempty"`   // e.g. "2.0"
	Disk   string `toml:"disk,omitempty"`   // e.g. "20G" — maps to --storage-opt size=
}

// AgentSpec is one agent running inside an island. An island hosts one or more
// agents; the first is the "primary" (the attach target for legacy clients).
type AgentSpec struct {
	ID   string `toml:"id"`   // stable per-island handle: "a1", "a2", …
	Type string `toml:"type"` // handler id: "claude-code", "codex", "headless"
	// Label is a user-facing, renamable name (e.g. "frontend"). Cosmetic.
	Label string `toml:"label,omitempty"`
	// Cmd is the entrypoint for headless agents; empty for the CLI agents.
	Cmd string `toml:"cmd,omitempty"`
	// Tmux is the in-container tmux session name for interactive agents. Empty
	// for headless. The migrated primary keeps "dejima" so a live attached
	// session survives a daemon upgrade; new agents use "agent-<id>".
	Tmux string `toml:"tmux,omitempty"`
	// Branch is the git branch backing this agent's worktree.
	Branch string `toml:"branch,omitempty"`
	// Worktree is the container path the agent works in: "/workspace" for the
	// primary, "/workspace/.agents/<id>" for the rest.
	Worktree string `toml:"worktree,omitempty"`
	// Restart enables supervise-and-restart-on-crash for co-located headless agents.
	Restart   bool      `toml:"restart,omitempty"`
	CreatedAt time.Time `toml:"created_at,omitempty"`
}

// Project is the persisted record for a single island.
type Project struct {
	Name string `toml:"name"`
	// Title is a cosmetic, freely-editable display name. Name stays the durable
	// infra handle (container/volume/network/config-dir identity, and the slug
	// addressed by the CLI); Title is what the user reads. Empty → show Name.
	Title   string `toml:"title,omitempty"`
	RepoURL string `toml:"repo"`
	// Agent and Cmd are the pre-multi-agent scalar fields. They are retained for
	// backward compatibility (older daemons read them) and mirror Agents[0]. New
	// code should read Agents; PrimaryAgent() is the accessor.
	Agent string `toml:"agent"`
	Image string `toml:"image"`
	// Cmd is the command to run inside the island when Agent is "headless".
	// It is ignored for the built-in CLI agents (claude-code, codex), which
	// have a baked-in command. Persisted so reset/reprovision can reuse it.
	Cmd          string      `toml:"cmd,omitempty"`
	Resources    Resources   `toml:"resources,omitempty"`
	CreatedAt    time.Time   `toml:"created_at"`
	LastUsedAt   time.Time   `toml:"last_used_at"`
	DesiredState State       `toml:"state"`
	Agents       []AgentSpec `toml:"agents,omitempty"`
	// Role is the island's purpose: "" (a work island) or "home" (a Home Island
	// hosting an assistant brain). Empty for islands created before roles existed.
	Role string `toml:"role,omitempty"`
	// Ports are brokered host-filesystem grants for this island (see ports.go).
	// Empty means deny-all: the island reaches no host content outside its repo.
	Ports []PortScope `toml:"ports,omitempty"`
}

// IsHome reports whether this island is a Home Island (hosts an assistant brain).
func (p *Project) IsHome() bool { return p.Role == RoleHome }

// EnsureAgents back-fills Agents from the legacy scalar Agent field for projects
// persisted under the pre-multi-agent schema. Idempotent: a no-op once Agents is
// populated. Called on Load and at provision time.
func (p *Project) EnsureAgents() {
	if len(p.Agents) > 0 || p.Agent == "" {
		return
	}
	spec := AgentSpec{
		ID:        "a1",
		Type:      p.Agent,
		Cmd:       p.Cmd,
		Worktree:  "/workspace",
		CreatedAt: p.CreatedAt,
	}
	if p.Agent != agentTypeHeadless {
		spec.Tmux = "agent-" + spec.ID // uniform with non-primary agents
	}
	p.Agents = []AgentSpec{spec}
}

// PrimaryAgent returns the island's first/primary agent (the attach target for
// legacy clients), or nil if the island has no agents.
func (p *Project) PrimaryAgent() *AgentSpec {
	if len(p.Agents) == 0 {
		return nil
	}
	return &p.Agents[0]
}

// AgentByID returns the agent with the given id.
func (p *Project) AgentByID(id string) (*AgentSpec, bool) {
	for i := range p.Agents {
		if p.Agents[i].ID == id {
			return &p.Agents[i], true
		}
	}
	return nil, false
}

// NextAgentID returns the next monotonic "a<N>" id not currently in use. Ids are
// never reused within an island's life, so a removed agent's id stays retired.
func (p *Project) NextAgentID() string {
	max := 0
	for _, a := range p.Agents {
		if n, ok := parseAgentID(a.ID); ok && n > max {
			max = n
		}
	}
	return fmt.Sprintf("a%d", max+1)
}

func parseAgentID(id string) (int, bool) {
	if len(id) < 2 || id[0] != 'a' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// AddAgent appends an agent to the island.
func (p *Project) AddAgent(spec AgentSpec) {
	p.Agents = append(p.Agents, spec)
}

// RemoveAgent drops the agent with the given id. Reports whether it was found.
func (p *Project) RemoveAgent(id string) bool {
	for i := range p.Agents {
		if p.Agents[i].ID == id {
			p.Agents = append(p.Agents[:i], p.Agents[i+1:]...)
			return true
		}
	}
	return false
}

// DisplayName is the user-facing name: the Title if set, else the Name slug.
func (p *Project) DisplayName() string {
	if p.Title != "" {
		return p.Title
	}
	return p.Name
}

// ContainerName returns the deterministic container name for this project.
func (p *Project) ContainerName() string {
	return "dejima-" + p.Name
}

// WorkspaceVolume returns the workspace volume name.
func (p *Project) WorkspaceVolume() string {
	return "dejima-" + p.Name + "-workspace"
}

// HomeVolume returns the per-island home-state volume, mounted at /home/dejima
// and shared by every agent in the island. Persisting the whole home means tool
// auth set once by any agent (Claude/Codex creds, ~/.npmrc, gh, eas/expo)
// survives restarts and is shared — the "collective permissioning" goal.
func (p *Project) HomeVolume() string {
	return "dejima-" + p.Name + "-home"
}

// NetworkName returns the per-island Docker network name.
func (p *Project) NetworkName() string {
	return "dejima-net-" + p.Name
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidateName ensures a name is safe to use in container/volume names.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, nameRe.String())
	}
	return nil
}

// DeriveNameFromRepo extracts a reasonable default name from a repo URL or path.
func DeriveNameFromRepo(repo string) string {
	s := strings.TrimSuffix(repo, "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)
	// Replace anything not in the allowed set with '-'.
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b = append(b, byte(r))
		default:
			b = append(b, '-')
		}
	}
	out := strings.Trim(string(b), "-.")
	if out == "" {
		out = "island"
	}
	return out
}

// Save writes the project config to disk.
func (p *Project) Save() error {
	path, err := paths.ProjectConfigPath(p.Name)
	if err != nil {
		return err
	}
	data, err := toml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project %q: %w", p.Name, err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads an existing project by name.
func Load(name string) (*Project, error) {
	// Validate before building any path: an unvalidated name (e.g. one carrying
	// a path separator or traversal, as a decoded "%2F" in a request can) would
	// be Clean-ed by filepath.Join into a different project's directory, letting
	// a caller scoped to one island read another's config. Names are validated
	// at create, so every legitimate on-disk project passes this.
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	path, err := paths.ProjectConfigPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("island %q not found", name)
		}
		return nil, err
	}
	var p Project
	if err := toml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("unmarshal project %q: %w", name, err)
	}
	p.EnsureAgents()
	return &p, nil
}

// Delete removes the project's on-host config directory.
func Delete(name string) error {
	dir, err := paths.ProjectDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// List returns every project the daemon knows about.
func List() ([]*Project, error) {
	dir, err := paths.ProjectsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := Load(e.Name())
		if err != nil {
			// Skip projects whose configs are missing or unreadable; surface elsewhere.
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Exists reports whether a project with this name has a config on disk.
func Exists(name string) bool {
	path, err := paths.ProjectConfigPath(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// EnsureProjectSubdirs creates intake/, exports/, logs/ for a project.
func EnsureProjectSubdirs(name string) error {
	dir, err := paths.ProjectDir(name)
	if err != nil {
		return err
	}
	for _, sub := range []string{"intake", "exports", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	return nil
}
