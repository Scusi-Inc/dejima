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
	// OOMPriority stack-ranks islands for the kernel OOM killer: higher = more
	// protected (killed later). nil = unset → resolved to a smart default at
	// create (headless brains start expendable). Mapped to docker --oom-score-adj
	// (inverted) in the api layer. Set-at-create only; a change needs a recreate.
	OOMPriority *int `toml:"oom_priority,omitempty"`
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
	Restart bool `toml:"restart,omitempty"`
	// Provider names which daemon LLM-provider credential this agent uses (see
	// internal/providercreds), e.g. "anthropic". Empty → the store default. Only
	// meaningful when the handler RequiresProviderKey.
	Provider string `toml:"provider,omitempty"`
	// Model is the "provider/model" string handed to the framework (via the
	// DEJIMA_MODEL env the per-agent shim translates). Empty → unset (the user
	// picks explicitly; there is no baked-in default).
	Model string `toml:"model,omitempty"`
	// Ephemeral marks an agent-spawned sub-agent (orchestrator pattern): it's
	// reaped automatically (on exit/TTL/parent-removal/revoke) and counts against
	// the island's spawn budget. SpawnedBy is the id of the agent that spawned it
	// (lineage; "" for operator-created agents). Co-located sub-agents share this
	// island's sandbox — they are NOT isolated from the parent.
	Ephemeral bool   `toml:"ephemeral,omitempty"`
	SpawnedBy string `toml:"spawned_by,omitempty"`
	// CreatedAt has no omitempty: go-toml/v2 omits a non-zero time.Time under
	// omitempty, which silently dropped this field on every save — so it must be
	// written unconditionally. The spawn reaper's TTL check depends on it surviving
	// a daemon reload.
	CreatedAt time.Time `toml:"created_at"`
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
	// NoHibernate pins the island awake: when true it is exempt from idle
	// auto-hibernate (it can still be hibernated manually). For a persistent
	// ambient agent — a watchtower / monitor that must keep running between bursts
	// of work — so an idle window doesn't shut it off. Default false.
	NoHibernate bool `toml:"no_hibernate,omitempty"`
	// Schedules are durable per-island scheduled wakes (see schedule.go). The
	// daemon's scheduler fires each when due, waking the island (and optionally
	// running a task) — surviving restart and `dejima upgrade`.
	Schedules []WakeSchedule `toml:"schedules,omitempty"`
	// Role is the island's purpose: "" (a work island) or "home" (a Home Island
	// hosting an assistant brain). Empty for islands created before roles existed.
	Role string `toml:"role,omitempty"`
	// GitHubIdentity names which of the daemon's GitHub identities this island
	// clones and pushes as (see internal/githubid). Empty means the daemon's
	// default identity, or — when the store is empty — the host's ~/.config/gh.
	GitHubIdentity string `toml:"github_identity,omitempty"`
	// Ports are brokered host-filesystem grants for this island (see ports.go).
	// Empty means deny-all: the island reaches no host content outside its repo.
	Ports []PortScope `toml:"ports,omitempty"`
	// Capabilities are brokered host-action grants for this island (see
	// capabilities.go and docs/capability-broker-spec.md). Empty means deny-all:
	// the island may invoke no host capabilities.
	Capabilities []CapabilityGrant `toml:"capabilities,omitempty"`
	// LinkActions are the named, typed action types THIS island exposes for
	// cross-island delegation (Lane 5, Phase 3). Another island may invoke one of
	// these only if it ALSO holds a link grant authorizing it (or an operator
	// approves ad hoc). Empty means deny-all: this island exposes no actions.
	LinkActions []string `toml:"link_actions,omitempty"`
	// Owner is a free-form creator label (e.g. "alice@laptop"), captured at
	// create time. Purely informational — there is no auth model yet — but it
	// lets wrapper dashboards attribute islands per person/team. Empty for
	// islands created before ownership existed.
	Owner string `toml:"owner,omitempty"`
	// Tags are free-form key=value labels (e.g. team=web, env=staging) for
	// grouping and per-team rollups in wrapper tooling. Empty when untagged.
	Tags map[string]string `toml:"tags,omitempty"`
	// BuiltVersion is the daemon version (version.Version) this island's container
	// was first created against, and UpgradedVersion is the version of the most
	// recent `dejima upgrade` recreate. They are the version-skew stamp: an island
	// whose UpgradedVersion (falling back to BuiltVersion) is behind the running
	// daemon was built from an older image and may carry stale /opt shims (the
	// socket→TCP heartbeat-break class of bug). The api layer sets these from
	// version.Version at create/upgrade; project stays a pure data struct. Empty
	// for islands created before this stamp existed ("unknown" provenance).
	BuiltVersion    string `toml:"built_version,omitempty"`
	UpgradedVersion string `toml:"upgraded_version,omitempty"`
	// Identity is the operator-chosen visual identity (color + glyph) override for
	// this island, persisted in config.toml. Nil/zero means no override — the TUI
	// then falls back to its deterministic per-name default. Set/cleared by the
	// operator via PUT/DELETE /v1/islands/{name}/identity. Cosmetic only.
	Identity Identity `toml:"identity,omitempty"`
	// HostGitHubGrant, when set, lets this island mount the HOST operator's own
	// ~/.config/gh — a credential whose read scope is the operator's entire
	// account. Nil means denied, which is the default for every island created
	// under the grant model. See github_host.go.
	HostGitHubGrant *HostGitHubGrant `toml:"host_github_grant,omitempty"`
	// HostGitHubReviewed records that the deny-by-default decision has been made
	// for this island — by creation under the grant model, by an operator
	// grant/revoke, or by the one-time migration. It is what stops a revoke from
	// being undone by re-grandfathering on the next Load.
	HostGitHubReviewed bool `toml:"host_github_reviewed,omitempty"`
}

// Identity is a per-island visual override: a hex color (#rgb or #rrggbb) and a
// single-rune glyph. The zero value (both empty) means unset. Validation lives in
// the api layer (PUT /v1/islands/{name}/identity); project stays a pure data struct.
type Identity struct {
	Color string `toml:"color,omitempty"`
	Glyph string `toml:"glyph,omitempty"`
}

// IsSet reports whether this island carries a visual-identity override (both
// color and glyph present).
func (i Identity) IsSet() bool {
	return i.Color != "" && i.Glyph != ""
}

// StampVersion returns the most authoritative version this island was last built
// or upgraded against: the upgrade stamp if present, else the build stamp. Empty
// when the island predates version stamping (provenance unknown).
func (p *Project) StampVersion() string {
	if p.UpgradedVersion != "" {
		return p.UpgradedVersion
	}
	return p.BuiltVersion
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

// NextAgentID returns the next monotonic "<letter><N>" id not currently in use.
// The letter is the island's mnemonic prefix (see agentIDPrefix), so an island
// named "Port" yields p1, p2, …. Ids are scoped per island and never reused
// within an island's life, so a removed agent's id stays retired. Numbering is
// monotonic across whatever prefixes already exist, so a legacy island that
// holds a1/a2 simply continues at the new prefix (p3).
func (p *Project) NextAgentID() string {
	max := 0
	for _, a := range p.Agents {
		if n, ok := parseAgentID(a.ID); ok && n > max {
			max = n
		}
	}
	return fmt.Sprintf("%s%d", agentIDPrefix(p.Name), max+1)
}

// UniqueAgentLabel returns a label not in use by any existing agent in this
// island, deduping case-insensitively on the trimmed value. If desired is empty
// (or all whitespace) it is returned as-is — empty labels are allowed and never
// deduped. Otherwise, on a collision it appends "-2", "-3", … skipping any
// variant already taken ("build" → "build-2" → "build-3"), mirroring the spirit
// of NextAgentID. exclude lets a rename ignore the agent being renamed so
// renaming to its own current label is a no-op (not "build-2"); pass "" at
// create time when there is nothing to exclude.
func (p *Project) UniqueAgentLabel(desired, excludeID string) string {
	trimmed := strings.TrimSpace(desired)
	if trimmed == "" {
		return desired
	}
	taken := func(candidate string) bool {
		for i := range p.Agents {
			if p.Agents[i].ID == excludeID {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(p.Agents[i].Label), candidate) {
				return true
			}
		}
		return false
	}
	if !taken(trimmed) {
		return trimmed
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", trimmed, n)
		if !taken(candidate) {
			return candidate
		}
	}
}

// genericAgentLabelBase is the default readable base used when an agent's Type
// is empty or yields no nicer name. It's the floor: DefaultAgentLabel never
// returns blank, so the worst case is "agent", "agent-2", …
const genericAgentLabelBase = "agent"

// agentLabelBase maps a known agent Type to a short, human-readable label base.
// It's intentionally small and self-contained (the data layer doesn't depend on
// the handlers registry) — the goal is a friendly stem like "claude" rather than
// the wire type "claude-code". A Type with no entry falls back to its own first
// path/word segment (see agentLabelBaseFor), and a blank/unknown Type to the
// generic base. Keep these lowercase, alnum, and collision-free with each other.
var agentLabelBase = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"shell":       "shell",
	"openclaw":    "openclaw",
	"letta":       "letta",
	"hermes":      "hermes",
	"goose":       "goose",
	"headless":    "agent",
}

// agentLabelBaseFor derives a readable label stem from an agent Type without any
// uniqueness handling. Known types use the curated agentLabelBase; an unknown
// (custom) type is reduced to a clean lowercase stem of its own string (the part
// before the first space or '/', alnum/'-' only); anything that reduces to empty
// falls back to the generic base. Never returns blank.
func agentLabelBaseFor(agentType string) string {
	t := strings.ToLower(strings.TrimSpace(agentType))
	if t == "" {
		return genericAgentLabelBase
	}
	if base, ok := agentLabelBase[t]; ok {
		return base
	}
	// Unknown/custom type (e.g. a bare command run as a generic agent): take the
	// leading word/path segment and keep it readable.
	if i := strings.IndexAny(t, " /\t"); i >= 0 {
		t = t[:i]
	}
	var b strings.Builder
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	stem := strings.Trim(b.String(), "-")
	if stem == "" {
		return genericAgentLabelBase
	}
	return stem
}

// DefaultAgentLabel derives the unique, non-blank label an agent should get when
// it is created (or backfilled) WITHOUT an explicit label. It maps the agent's
// Type to a readable base (e.g. "claude-code" → "claude", unknown → "agent") and
// runs that base through UniqueAgentLabel so the stored default never collides
// with an existing agent ("claude", "claude-2", …). It NEVER returns blank.
// exclude lets a caller ignore one agent (e.g. the one being (re)labeled) when
// checking for collisions; pass "" at create time.
func (p *Project) DefaultAgentLabel(spec AgentSpec, excludeID string) string {
	return p.UniqueAgentLabel(agentLabelBaseFor(spec.Type), excludeID)
}

// BackfillAgentLabels assigns a derived default label to every agent whose Label
// is blank (empty or all-whitespace), so islands created before default labels
// existed get readable names with no manual step. It is:
//   - idempotent: agents that already have a label are left untouched, so a second
//     run is a no-op (it never re-derives or appends "-2" to a settled label);
//   - order-stable: it walks Agents in slice order and derives each label against
//     the labels already present (including ones it just assigned), so the same
//     island always yields the same names; and
//   - collision-safe within the island: two blank "claude-code" agents become
//     "claude" and "claude-2", never two "claude".
//
// It mutates in place and returns whether it changed anything, so callers can
// persist (Save) only when needed and avoid a write storm on already-backfilled
// islands.
func (p *Project) BackfillAgentLabels() (changed bool) {
	for i := range p.Agents {
		if strings.TrimSpace(p.Agents[i].Label) != "" {
			continue
		}
		// Derive against the current state of the list (earlier agents already
		// have labels, later blank ones get theirs on subsequent iterations), so
		// the result is unique and order-stable. excludeID is the agent's own id so
		// its (blank) slot doesn't shadow the lookup.
		p.Agents[i].Label = p.DefaultAgentLabel(p.Agents[i], p.Agents[i].ID)
		changed = true
	}
	return changed
}

// agentIDPrefix is the per-island letter that leads its agent ids: the first
// ASCII letter of the island name, lowercased ("Port" → "p"). It is purely a
// mnemonic — ids are island-scoped and addressed as island/<id>, so two islands
// sharing a first letter never actually collide. Names with no leading letter
// (e.g. "123") fall back to "a".
func agentIDPrefix(name string) string {
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'A' && c <= 'Z':
			return string(c - 'A' + 'a')
		case c >= 'a' && c <= 'z':
			return string(c)
		}
	}
	return "a"
}

// PrimaryAgentID is the id a brand-new island's primary agent gets: the island's
// mnemonic letter + "1" (e.g. "Port" → "p1"), matching the scheme NextAgentID
// uses for added agents. Legacy islands migrated by EnsureAgents keep "a1" so a
// live attached session isn't renamed out from under the user.
func PrimaryAgentID(name string) string {
	return agentIDPrefix(name) + "1"
}

// SetPrimaryID renames the primary agent's id and the tmux session derived from
// it. Intended for fresh provision only — before any container or session
// exists — so it deliberately does not migrate a running session.
func (p *Project) SetPrimaryID(id string) {
	if len(p.Agents) == 0 {
		return
	}
	a := &p.Agents[0]
	a.ID = id
	if a.Tmux != "" {
		a.Tmux = "agent-" + id
	}
}

// parseAgentID extracts the trailing number of an agent id like "a1" or "p3".
// Ids are <letters><number>; the leading letters are an island mnemonic and are
// ignored here so numbering stays monotonic even across a prefix change.
func parseAgentID(id string) (int, bool) {
	i := 0
	for i < len(id) && ((id[i] >= 'a' && id[i] <= 'z') || (id[i] >= 'A' && id[i] <= 'Z')) {
		i++
	}
	if i == 0 || i == len(id) {
		return 0, false
	}
	n, err := strconv.Atoi(id[i:])
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

// MoveAgent shifts the agent with the given id by delta positions within the
// list (negative = toward the front), clamping to the ends. Reports whether the
// agent was found and actually moved. Order is cosmetic — the dashboard and CLI
// no longer key off position — except that Agents[0] still seeds the container
// entrypoint on the next recreate (see docs/island-pid1-unification.md).
func (p *Project) MoveAgent(id string, delta int) bool {
	from := -1
	for i := range p.Agents {
		if p.Agents[i].ID == id {
			from = i
			break
		}
	}
	if from < 0 {
		return false
	}
	to := from + delta
	if to < 0 {
		to = 0
	}
	if to > len(p.Agents)-1 {
		to = len(p.Agents) - 1
	}
	if to == from {
		return false
	}
	a := p.Agents[from]
	p.Agents = append(p.Agents[:from], p.Agents[from+1:]...)
	// Re-insert at the clamped target.
	p.Agents = append(p.Agents[:to], append([]AgentSpec{a}, p.Agents[to:]...)...)
	return true
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
	// Defense in depth: never resolve a name with a separator or traversal into a
	// config path (filepath.Join/Clean would silently retarget another island).
	// All real island names pass ValidateName at creation, so this only rejects
	// malformed input — e.g. an encoded-slash bypass attempt. See tokenauth.go.
	if err := ValidateName(name); err != nil {
		return nil, fmt.Errorf("invalid island name %q: %w", name, err)
	}
	path, err := paths.ProjectConfigPathRead(name)
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
	// This is the single load-time choke point every Project flows through, so
	// one-time migrations live here — idempotent, persisted only on an actual
	// change, so steady-state Loads do no extra writes.
	changed := p.BackfillAgentLabels() // agents predating default-labeling get a name
	// Multi-tenant ownership migration: an island with no owner predates ownership
	// (or an older daemon) — attribute it to the host owner so it stays managed by
	// the operator and private from teammates.
	if strings.TrimSpace(p.Owner) == "" {
		p.Owner = HostOwner()
		changed = true
	}
	// Deny-by-default cutover for the host operator's gh credential. Runs AFTER
	// the ownership backfill above, which it depends on to tell a host island
	// from a tenant one.
	if p.migrateHostGitHub() {
		changed = true
	}
	if changed {
		if err := p.Save(); err != nil {
			return nil, fmt.Errorf("migrate project %q on load: %w", name, err)
		}
	}
	return &p, nil
}

// HostOwner is the tenant id attributed to the host operator: the owner of
// islands created via the trusted local socket or a RoleOwner token, and the
// backfill value for islands that predate ownership. Configurable via
// DEJIMA_HOST_OWNER; defaults to "aoos".
func HostOwner() string {
	if v := strings.TrimSpace(os.Getenv("DEJIMA_HOST_OWNER")); v != "" {
		return v
	}
	return "aoos"
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
	path, err := paths.ProjectConfigPathRead(name)
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
