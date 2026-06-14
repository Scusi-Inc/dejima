package api

import (
	"time"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/ledger"
)

// IslandInfo is the public view of an island returned by the API.
type IslandInfo struct {
	Name string `json:"name"`
	// Title is the cosmetic display name; empty means show Name. Name remains the
	// durable handle the CLI addresses by.
	Title string `json:"title,omitempty"`
	Repo  string `json:"repo"`
	// Agent is the island's agent type (e.g. "claude-code", "codex", "headless").
	Agent string `json:"agent"`
	Image string `json:"image"`
	// Cmd is the user-supplied entrypoint for headless islands; empty for
	// the built-in CLI agents.
	Cmd string `json:"cmd,omitempty"`
	// Role is "" (work island) or "home" (a Home Island hosting an assistant brain).
	Role       string          `json:"role,omitempty"`
	State      string          `json:"state"`     // desired state from config
	Container  string          `json:"container"` // observed status from runtime
	CreatedAt  time.Time       `json:"created_at"`
	LastUsedAt time.Time       `json:"last_used_at"`
	Attached   []PresenceEntry `json:"attached,omitempty"`
	Stats      *IslandStats    `json:"stats,omitempty"`
	AgentState *AgentStateInfo `json:"agent_state,omitempty"`
	Git        *GitInfo        `json:"git,omitempty"`
	Health     *IslandHealth   `json:"health,omitempty"`
	// Agents is the island's agents. For islands created before multi-agent
	// support it carries a single synthesized entry mirroring Agent.
	Agents []AgentInfo `json:"agents,omitempty"`
}

// AgentInfo is the public view of one agent within an island.
type AgentInfo struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Label      string `json:"label,omitempty"`
	Tmux       string `json:"tmux,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Attachable bool   `json:"attachable"`
	// CreatedAt is when the agent was added to the island — the basis for its
	// displayed uptime/age. Zero for legacy agents persisted before this field.
	CreatedAt time.Time `json:"created_at,omitempty"`
	// State is the agent's session liveness ("running"/"stopped"/"").
	State      string          `json:"state,omitempty"`
	AgentState *AgentStateInfo `json:"agent_state,omitempty"`
	Attached   []PresenceEntry `json:"attached,omitempty"`
	// Error is the last orchestration failure for this agent — e.g. its worktree
	// or tmux session couldn't be created. Empty when the agent came up cleanly.
	Error   string    `json:"error,omitempty"`
	ErrorAt time.Time `json:"error_at,omitempty"`
}

// IslandHealth surfaces crash-relevant facts that a remote client can't observe
// itself (they require container-engine access). Populated on the detail
// endpoint only. RestartCount > 0 or OOMKilled signal an unhealthy island.
type IslandHealth struct {
	OOMKilled    bool `json:"oom_killed"`
	RestartCount int  `json:"restart_count"`
	ExitCode     int  `json:"exit_code,omitempty"`
}

// IslandStats is a snapshot of the container's resource usage.
type IslandStats struct {
	MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
	CPUPercent       float64 `json:"cpu_percent"`
}

// AgentStateInfo is the most recent operational signal from the agent itself,
// derived from agent-event hooks (currently emitted by the Claude Code shim).
// Latest may be empty if the agent hasn't emitted any events.
type AgentStateInfo struct {
	Latest    string    `json:"latest"` // e.g. "waiting-for-input", "task-complete", "error"
	UpdatedAt time.Time `json:"updated_at"`
}

// GitInfo summarizes the workspace's git state. Only populated on the detail
// endpoint (GET /v1/islands/:name) and only for running islands. Computed
// lazily via container exec and cached briefly to avoid spamming the island.
type GitInfo struct {
	Branch     string `json:"branch"`
	Clean      bool   `json:"clean"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	DirtyFiles int    `json:"dirty_files"`
}

// OverviewResponse is the body of GET /v1/overview — server-wide totals
// plus substrate health (Docker reachable, island image present).
type OverviewResponse struct {
	TotalIslands       int       `json:"total_islands"`
	Running            int       `json:"running"`
	Hibernated         int       `json:"hibernated"`
	Errored            int       `json:"errored"`
	AttachedClients    int       `json:"attached_clients"`
	MemoryUsageBytes   uint64    `json:"memory_usage_bytes"`
	MemoryLimitBytes   uint64    `json:"memory_limit_bytes"`
	CPUPercent         float64   `json:"cpu_percent"`
	DaemonStartedAt    time.Time `json:"daemon_started_at"`
	WebhookCount       int       `json:"webhook_count"`
	DockerReachable    bool      `json:"docker_reachable"`
	IslandImagePresent bool      `json:"island_image_present"`
	IslandImage        string    `json:"island_image,omitempty"`
	// HostTerminalsEnabled lets a client (the TUI) show the Host section only
	// when the daemon was started with --host-terminals.
	HostTerminalsEnabled bool `json:"host_terminals_enabled"`
	// SSHAddr is the SSH-façade listen addr, empty unless dejimad was started
	// with --ssh. Lets clients (the TUI, `dejima ssh config/info`) show the
	// connection target and generate an ssh config entry. The bind host may be
	// wildcard/empty (":2222"); clients resolve a reachable host themselves.
	SSHAddr string `json:"ssh_addr,omitempty"`
	// DaemonVersion / APIVersion let a client detect skew against the daemon.
	// APIVersion is 0 from daemons predating version reporting.
	DaemonVersion string `json:"daemon_version,omitempty"`
	APIVersion    int    `json:"api_version,omitempty"`
}

// AdminUpdateRequest is the body of POST /v1/admin/update. Execute=false (the
// default) reports the plan without changing anything.
type AdminUpdateRequest struct {
	Execute bool `json:"execute"`
}

// AdminUpdateResponse reports the daemon's update status and, when Execute was
// set and an update is available, that the apply has started (the daemon then
// restarts, so this response is the client's confirmation it began).
type AdminUpdateResponse struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	Mode            string `json:"mode"` // source | release
	UpdateAvailable bool   `json:"update_available"`
	Applying        bool   `json:"applying"`
}

// CreateIslandRequest is the body of POST /v1/islands.
type CreateIslandRequest struct {
	Name      string    `json:"name,omitempty"`  // optional; derived from repo if empty
	Repo      string    `json:"repo"`            // required
	Agent     string    `json:"agent,omitempty"` // defaults to "claude-code"
	Image     string    `json:"image,omitempty"` // defaults to "dejima/island:latest"
	Resources Resources `json:"resources,omitempty"`
	// SeedPath, when set, is a host path bind-mounted read-only as the clone
	// source (see reposrc local-copy mode). Only valid against a local daemon;
	// Repo then holds the upstream URL to set as origin, or "" for no remote.
	SeedPath string `json:"seed_path,omitempty"`
	// Cmd is the entrypoint command for agent="headless" islands (e.g.
	// "python my_loop.py"). Required when Agent is "headless"; ignored
	// otherwise. The container runs the command via /bin/sh -c, so shell
	// quoting applies.
	Cmd string `json:"cmd,omitempty"`
	// Agents, when non-empty, seeds the island with multiple agents: element 0
	// is the primary, the rest are added as co-located agents at provision time.
	// When empty, the scalar Agent/Cmd above describe a single agent (back-compat).
	Agents []AgentSpecRequest `json:"agents,omitempty"`
	// Role is the island's purpose: "" (work island, default) or "home" (a Home
	// Island hosting an assistant brain). A home island must be headless (+cmd).
	Role string `json:"role,omitempty"`
	// GitHubIdentity names which daemon GitHub identity this island clones and
	// pushes as (see GET /v1/credentials/github). Empty uses the daemon default,
	// or the host's ~/.config/gh when no identities are configured.
	GitHubIdentity string `json:"github_identity,omitempty"`
}

// CreateIslandResponse is the result of POST /v1/islands: an IslandInfo
// (JSON-flattened via embedding) plus, only on a token-authenticated create by
// a Home Island, the new island's bearer Token. This is the parent-child spawn
// model — the parent brain receives the child's token and drives it over the
// same autonomy path, so there is no god-token. Operator-driven creates (unix
// socket / tailnet) leave Token empty, making the JSON byte-identical to a bare
// IslandInfo for existing clients.
type CreateIslandResponse struct {
	IslandInfo
	Token string `json:"token,omitempty"`
}

// UpdateIslandRequest is the body of PATCH /v1/islands/{name}. Only cosmetic,
// in-place-editable fields live here (Name and infra identity are immutable).
type UpdateIslandRequest struct {
	Title string `json:"title"`
}

// AgentSpecRequest describes one agent to create — either as an element of
// CreateIslandRequest.Agents or the body of POST /v1/islands/{name}/agents.
type AgentSpecRequest struct {
	Type  string `json:"type,omitempty"`  // defaults to the island/default agent
	Label string `json:"label,omitempty"` // optional, renamable
	Cmd   string `json:"cmd,omitempty"`   // required only for headless
}

// Resources mirrors project.Resources for API transport.
type Resources struct {
	Memory string `json:"memory,omitempty"`
	CPUs   string `json:"cpus,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

// ExecRequest is the body of POST /v1/islands/:name/exec.
type ExecRequest struct {
	Cmd []string `json:"cmd"`
}

// ExecResponse is returned by POST /v1/islands/:name/exec.
type ExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// PushCredentialsRequest is the body of PUT /v1/credentials/claude.
// CredentialsJSON is the verbatim content of a Claude Code credentials file
// (the {"claudeAiOauth": ...} blob).
type PushCredentialsRequest struct {
	CredentialsJSON string `json:"credentials_json"`
}

// ClaudeCredentialsStatus is the body of GET /v1/credentials/claude.
// It never carries the secret itself.
type ClaudeCredentialsStatus struct {
	// SeedPresent reports whether a materialized seed file exists, i.e.
	// whether new islands will start with Claude credentials.
	SeedPresent   bool      `json:"seed_present"`
	SeedUpdatedAt time.Time `json:"seed_updated_at,omitempty"`
	// HostSource is where the daemon host can read credentials right now:
	// "keychain", "file", or "" when the host has no Claude login (the seed
	// then only refreshes via `dejima auth push`).
	HostSource string `json:"host_source,omitempty"`
}

// GitHubIdentitiesResponse is the body of GET /v1/credentials/github: the
// daemon's GitHub identities without their tokens.
type GitHubIdentitiesResponse struct {
	Identities []githubid.Meta `json:"identities"`
}

// PutGitHubIdentityRequest is the body of PUT /v1/credentials/github/:name —
// how a client (e.g. `dejima auth push --github`) seeds or updates an identity.
type PutGitHubIdentityRequest struct {
	Login   string `json:"login"`
	Host    string `json:"host,omitempty"` // defaults to github.com
	Token   string `json:"token"`
	Default bool   `json:"default,omitempty"` // make this the default identity
}

// DeleteGitHubIdentityResponse reports which islands still referenced the
// identity that was just deleted. Those islands keep working until their next
// credential reseed (reset/upgrade), at which point they fall back to the host
// gh or lose push auth — so the caller should warn about them.
type DeleteGitHubIdentityResponse struct {
	AffectedIslands []string `json:"affected_islands,omitempty"`
}

// GitHubReposResponse is the body of GET /v1/credentials/github/:name/repos.
// Capped is true when the identity can see more repos than the single page
// returned, so the browser can say "showing the first N".
type GitHubReposResponse struct {
	Repos  []githubid.Repo `json:"repos"`
	Capped bool            `json:"capped,omitempty"`
}

// PortScopeRequest is the body of POST /v1/islands/:name/port/scopes.
type PortScopeRequest struct {
	HostPath string `json:"host_path"`
	Mode     string `json:"mode"` // "ro" (V1); "rw" is rejected until the read-write milestone
}

// PortScopeView is one brokered host-file grant as returned by the API.
type PortScopeView struct {
	Name      string    `json:"name"`
	HostPath  string    `json:"host_path"`
	Mode      string    `json:"mode"`
	GrantedAt time.Time `json:"granted_at"`
}

// PortScopesResponse is the body of GET /v1/islands/:name/port/scopes.
type PortScopesResponse struct {
	Scopes []PortScopeView `json:"scopes"`
}

// PortIntakeRequest is the body of POST /v1/islands/:name/port/intake — a
// brokered, read-only copy of a host file (within a granted scope) into the
// island.
type PortIntakeRequest struct {
	Scope  string `json:"scope"`          // scope name to read from
	SrcRel string `json:"src_rel"`        // path relative to the scope's host root
	Dest   string `json:"dest,omitempty"` // container path; default /intake/<scope>/<src_rel>
}

// PortIntakeResponse reports a completed intake.
type PortIntakeResponse struct {
	Scope  string `json:"scope"`
	Src    string `json:"src"`  // resolved host path
	Dest   string `json:"dest"` // container path
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// PortExportRequest is the body of POST /v1/islands/:name/port/export — a
// brokered copy of a file out of the island into the host-owned export staging
// area (~/.dejima/projects/<name>/exports/). It never writes into a user scope;
// writing into a granted scope is the read-write milestone.
type PortExportRequest struct {
	Src string `json:"src"` // container path to export
}

// PortExportResponse reports a completed export to staging.
type PortExportResponse struct {
	Src    string `json:"src"`  // container path
	Dest   string `json:"dest"` // host staging path
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// AuditResponse is the body of GET /v1/audit — the brokered-operation Ledger,
// plus the result of verifying its hash chain.
type AuditResponse struct {
	Entries  []ledger.Entry `json:"entries"`
	Total    int            `json:"total"`
	Verified bool           `json:"verified"`
	Error    string         `json:"error,omitempty"` // chain-verification failure detail
}

// PortWriteRequest is the body of POST /v1/islands/:name/port/write — copy a
// file out of the island INTO a read-write scope on the host.
type PortWriteRequest struct {
	Scope   string `json:"scope"`
	Src     string `json:"src"`      // container path
	DestRel string `json:"dest_rel"` // path within the scope
}

// PortWriteResponse reports a completed write into a scope.
type PortWriteResponse struct {
	Scope  string `json:"scope"`
	Src    string `json:"src"`  // container path
	Dest   string `json:"dest"` // host path written
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ErrorResponse is the body of any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}
