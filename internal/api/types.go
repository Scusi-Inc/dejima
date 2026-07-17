package api

import (
	"time"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/providercreds"
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
	Role      string            `json:"role,omitempty"`
	Owner     string            `json:"owner,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	State     string            `json:"state"`     // desired state from config
	Container string            `json:"container"` // observed status from runtime
	// NoHibernate is true when the island is pinned awake (exempt from idle
	// auto-hibernate). Set via PATCH /v1/islands/{name} (dejima pin/unpin).
	NoHibernate bool            `json:"no_hibernate,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	LastUsedAt  time.Time       `json:"last_used_at"`
	Attached    []PresenceEntry `json:"attached,omitempty"`
	Stats       *IslandStats    `json:"stats,omitempty"`
	AgentState  *AgentStateInfo `json:"agent_state,omitempty"`
	Git         *GitInfo        `json:"git,omitempty"`
	Health      *IslandHealth   `json:"health,omitempty"`
	Disk        *IslandDisk     `json:"disk,omitempty"`
	// Resources are the island's configured caps + OOM priority (nil OOMPriority
	// means the smart default applies). Present on both the list and detail
	// endpoints — cheap (read from island config) and needed alongside Stats so a
	// client can compute usage as a "% of cap".
	Resources *Resources `json:"resources,omitempty"`
	// Agents is the island's agents. For islands created before multi-agent
	// support it carries a single synthesized entry mirroring Agent.
	Agents []AgentInfo `json:"agents,omitempty"`
	// BuiltVersion / UpgradedVersion are the version-skew stamp: the daemon build
	// the island's container was first created against, and the build of its most
	// recent `dejima upgrade` recreate. A stamp behind the running daemon means the
	// island was built from an older image and may carry stale /opt shims. Both
	// empty for islands created before version stamping (provenance unknown).
	BuiltVersion    string `json:"built_version,omitempty"`
	UpgradedVersion string `json:"upgraded_version,omitempty"`
	// NeverHeardFrom is the zero-heartbeat liveness flag: true when the island's
	// container is running yet NO agent has emitted a single agent-state event
	// since boot, and the island is past a short grace window (so a just-started
	// island isn't falsely flagged). This is the direct broken-shim signal — a
	// stale socket→TCP notify hook silently no-ops, so the heartbeat never fires
	// and mail-nudges / idle-hibernate / the idle metric all go dark with no error.
	NeverHeardFrom bool `json:"never_heard_from,omitempty"`
	// Identity is the operator-set visual identity (color + glyph) for the island.
	// Omitted when unset — the TUI then falls back to its deterministic per-name
	// default (islandIdentity). Set/cleared via PUT /v1/islands/{name}/identity.
	// (Backend populate + the PUT route are d5's; this field is the shared seam.)
	Identity *IslandIdentity `json:"identity,omitempty"`
}

// IslandIdentity is an operator-chosen color + glyph override for an island.
// Color is a hex string (#rgb or #rrggbb); Glyph is exactly one rune.
type IslandIdentity struct {
	Color string `json:"color"`
	Glyph string `json:"glyph"`
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
	// State is the agent's session liveness: "running", "stopped" (no tmux
	// session), "exited" (session alive but the agent process died and only a
	// shell prompt remains), or "" (not probed). Detail endpoint only.
	State      string          `json:"state,omitempty"`
	AgentState *AgentStateInfo `json:"agent_state,omitempty"`
	Attached   []PresenceEntry `json:"attached,omitempty"`
	// Restarts is how many times a supervised (Restart) headless agent has
	// crashed and been respawned by its supervisor loop — counted from the
	// per-agent log. A climbing count is how a crash-loop (e.g. OOM) shows up,
	// since a supervised agent's session stays "running". Detail endpoint only.
	Restarts int `json:"restarts,omitempty"`
	// Error is the last orchestration failure for this agent — e.g. its worktree
	// or tmux session couldn't be created. Empty when the agent came up cleanly.
	Error   string    `json:"error,omitempty"`
	ErrorAt time.Time `json:"error_at,omitempty"`
	// Provider/Model echo the agent's configured LLM target (only meaningful for
	// frameworks that reach a model over a provider API key; empty otherwise).
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// ProviderKeySet reports whether the daemon has a provider credential to
	// inject for this agent's provider.
	ProviderKeySet bool `json:"provider_key_set,omitempty"`
	// AuthState is the proactive LLM-credential readiness, computed from the
	// handler registry + provider store (never from logs): "missing-provider-auth"
	// when a key-requiring agent has no resolvable key (it will fail at first
	// task), else "" (ready, or the agent needs no provider key).
	AuthState string `json:"auth_state,omitempty"`
	// Usage is the agent's adapter-REPORTED token/cost (Claude Code today).
	// OMITTED entirely for adapters that don't report — clients render "n/a"
	// rather than a fake zero. Detail endpoint only.
	Usage *AgentUsage `json:"usage,omitempty"`
	// Ephemeral / SpawnedBy surface an agent-spawned sub-agent and its lineage
	// (the spawning agent's id). Empty/false for operator-created agents.
	// SpawnedByLabel is the spawner's human name, so lineage renders as a name.
	Ephemeral      bool   `json:"ephemeral,omitempty"`
	SpawnedBy      string `json:"spawned_by,omitempty"`
	SpawnedByLabel string `json:"spawned_by_label,omitempty"`
}

// RefID / RefLabel let an AgentInfo satisfy project.AgentRef, so the CLI can
// resolve a user-supplied agent ref (id or label) against the island's agent
// list with the same shared resolver the daemon uses.
func (a AgentInfo) RefID() string    { return a.ID }
func (a AgentInfo) RefLabel() string { return a.Label }

// AgentUsage is an agent's self-reported token/cost for its session, ingested
// from the agent's own usage hook over the in-island token path. Dejima can't
// observe the (opaque, outbound) LLM call, so these numbers come FROM the agent;
// an adapter that doesn't report leaves AgentInfo.Usage nil → "n/a" (we never
// fake uniform coverage). InputTokens aggregates fresh + cached input so
// InputTokens + OutputTokens == TotalTokens. CostUSD is nil when the model
// isn't in the price table (tokens still show; cost renders n/a).
type AgentUsage struct {
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostUSD      *float64  `json:"cost_usd,omitempty"`
	Source       string    `json:"source"` // reporting adapter, e.g. "claude-code"
	AsOf         time.Time `json:"as_of"`
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
	// Panicked is true while the ~/.dejima/PANIC flag is set: every island is
	// stopped and the daemon won't auto-start them until panic is cleared.
	Panicked bool `json:"panicked,omitempty"`
	// Substrate memory: HostMemoryBytes is the daemon host's physical RAM;
	// VMMemoryBytes is the container runtime's memory ceiling (the colima/Docker
	// Desktop VM total on macOS — the pool ALL islands share); VMRecommendedBytes
	// is the size dejima suggests for this host. When the VM is far below the
	// recommendation the TUI raises a substrate banner — a too-small VM is the
	// root cause of island OOMs (#23). All 0 when undeterminable.
	HostMemoryBytes    uint64 `json:"host_memory_bytes,omitempty"`
	VMMemoryBytes      uint64 `json:"vm_memory_bytes,omitempty"`
	VMRecommendedBytes uint64 `json:"vm_recommended_bytes,omitempty"`
	// Owner / Role identify the AUTHENTICATED caller (multi-tenant "who am I"), so
	// a client can drive the own-vs-all lens: the host owner (role "owner") sees
	// all islands and can filter to Owner; a teammate is already server-filtered.
	// Empty on callers without a resolved identity.
	Owner string `json:"owner,omitempty"`
	Role  string `json:"role,omitempty"`
}

// AggregateResponse is the privacy-preserving host-wide rollup returned by GET
// /v1/aggregate (multi-tenant design, capRead + any authenticated caller). It
// carries counts + totals across ALL islands and NEVER any names, repos, owners,
// or per-island rows — so a teammate can see shared-host utilization without
// seeing what's running. Field tags are the locked contract between the client
// (this type, a2) and the server handler (a1's P3). Memory fields are uint64 to
// match OverviewResponse; disk is int64 to match disk.total_bytes.
type AggregateResponse struct {
	TotalIslands     int     `json:"total_islands"`
	Running          int     `json:"running"`
	Hibernated       int     `json:"hibernated"`
	MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
	CPUPercent       float64 `json:"cpu_percent"`
	DiskTotalBytes   int64   `json:"disk_total_bytes"`
}

// AdminUpdateRequest is the body of POST /v1/admin/update. Execute=false (the
// default) reports the plan without changing anything.
type AdminUpdateRequest struct {
	Execute bool `json:"execute"`
	// Force applies the update even while terminal sessions are attached. The
	// daemon restart drops every attached client, so by default an Execute with
	// clients attached is deferred (Deferred=true) instead of yanking them.
	Force bool `json:"force,omitempty"`
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
	// Deferred is set when an available update was NOT applied because terminal
	// sessions are attached and Force was not set; AttachedClients is how many.
	// The caller retries with Force (or once the terminals detach) to apply.
	Deferred        bool `json:"deferred,omitempty"`
	AttachedClients int  `json:"attached_clients,omitempty"`
}

// AuthorizeSSHKeyRequest authorizes a public key fleet-wide via the operator
// API, so any operator device can enroll its own key without copying it to the
// daemon host (and the daemon — which owns the file — performs the write).
type AuthorizeSSHKeyRequest struct {
	PublicKey string `json:"public_key"` // an OpenSSH "ssh-… AAAA… [comment]" line
}

// AuthorizeSSHKeyResponse returns the enrolled key's fingerprint.
type AuthorizeSSHKeyResponse struct {
	Fingerprint string `json:"fingerprint"`
}

// SSHKeyInfo is one authorized key, for listing.
type SSHKeyInfo struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Comment     string `json:"comment"`
}

// ListSSHKeysResponse is the set of fleet-wide authorized keys.
type ListSSHKeysResponse struct {
	Keys []SSHKeyInfo `json:"keys"`
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
	// Owner is a free-form creator label (e.g. "alice@laptop") and Tags are
	// free-form key=value labels (team=web, …); both are informational metadata
	// surfaced in IslandInfo for wrapper dashboards. Optional.
	Owner string            `json:"owner,omitempty"`
	Tags  map[string]string `json:"tags,omitempty"`
	// AllowNoIdentity overrides the doomed-private-clone gate: normally a remote
	// repo that isn't anonymously cloneable and has no GitHub identity is rejected
	// at create (it would come up as an empty, repo-less island). Set true (CLI
	// `--force`) to create anyway and authenticate later.
	AllowNoIdentity bool `json:"allow_no_identity,omitempty"`
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
// Fields are pointers so a request applies ONLY what it sends — a no_hibernate
// update doesn't clobber the title, and vice-versa.
type UpdateIslandRequest struct {
	Title *string `json:"title,omitempty"`
	// NoHibernate pins the island awake (exempt from idle auto-hibernate). nil
	// leaves the current setting unchanged.
	NoHibernate *bool `json:"no_hibernate,omitempty"`
}

// CreateScheduleRequest is the body of POST /v1/islands/{name}/schedules. Exactly
// one of Every (recurring Go duration, e.g. "720h") or At (one-shot RFC3339 time)
// is required. Task is an optional prompt injected into Agent (id/label; ""=the
// primary) once the island wakes.
type CreateScheduleRequest struct {
	Every string `json:"every,omitempty"`
	At    string `json:"at,omitempty"`
	Task  string `json:"task,omitempty"`
	Agent string `json:"agent,omitempty"`
}

// ScheduleInfo is the public view of a wake schedule.
type ScheduleInfo struct {
	ID        string    `json:"id"`
	Every     string    `json:"every,omitempty"`
	Task      string    `json:"task,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	NextDue   time.Time `json:"next_due"`
	LastRun   time.Time `json:"last_run,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentSpecRequest describes one agent to create — either as an element of
// CreateIslandRequest.Agents or the body of POST /v1/islands/{name}/agents.
type AgentSpecRequest struct {
	Type  string `json:"type,omitempty"`  // defaults to the island/default agent
	Label string `json:"label,omitempty"` // optional, renamable
	Cmd   string `json:"cmd,omitempty"`   // required only for headless
	// Provider/Model select the LLM target for key-requiring frameworks. Provider
	// names a daemon credential (see /v1/credentials/providers); Model is the
	// "provider/model" string. Both optional; only meaningful when the agent type
	// RequiresProviderKey.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Ephemeral requests an auto-reaped sub-agent. An in-island token (an
	// agent-initiated spawn) MUST set this — a token may only create ephemeral
	// sub-agents within the operator's spawn grant, never persistent agents.
	// SpawnedBy is the spawning agent's id (lineage / depth-cap input).
	Ephemeral bool   `json:"ephemeral,omitempty"`
	SpawnedBy string `json:"spawned_by,omitempty"`
}

// Resources mirrors project.Resources for API transport.
type Resources struct {
	Memory string `json:"memory,omitempty"`
	CPUs   string `json:"cpus,omitempty"`
	Disk   string `json:"disk,omitempty"`
	// OOMPriority stack-ranks islands for the OOM killer: higher = more protected
	// (killed later). nil = unset → smart default at create (headless brains start
	// expendable). Maps to docker --oom-score-adj (inverted) in the daemon.
	OOMPriority *int `json:"oom_priority,omitempty"`
}

// UpdateResourcesRequest is the body of PUT /v1/islands/:name/resources. Pointer
// fields distinguish "leave unchanged" (nil) from an explicit value (incl. ""
// for Memory → unlimited).
type UpdateResourcesRequest struct {
	Memory      *string `json:"memory,omitempty"`
	OOMPriority *int    `json:"oom_priority,omitempty"`
}

// UpdateResourcesResponse echoes the stored resources and flags whether a change
// needs a container recreate to take effect (true when OOMPriority changed —
// --oom-score-adj is set at create only; Memory applies live via docker update).
type UpdateResourcesResponse struct {
	Resources       Resources `json:"resources"`
	RestartRequired bool      `json:"restart_required"`
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

// IslandDisk reports an island's on-disk volume usage (detail endpoint only;
// from `docker system df -v`, cached). Bytes are 0 when the storage driver
// doesn't report size. WorkspaceBytes is the code volume, HomeBytes the
// per-island home (creds + agent state); Total is their sum.
type IslandDisk struct {
	WorkspaceBytes int64 `json:"workspace_bytes"`
	HomeBytes      int64 `json:"home_bytes"`
	TotalBytes     int64 `json:"total_bytes"`
}

// CloneIslandRequest is the body of POST /v1/islands/{name}/clone.
type CloneIslandRequest struct {
	NewName string `json:"new_name"`
}

// PanicRequest is the optional body of POST /v1/panic.
type PanicRequest struct {
	Reason string `json:"reason,omitempty"`
}

// PanicResponse is returned by the /v1/panic endpoints. Affected is the number
// of islands stopped (on engage) or restarted (on clear).
type PanicResponse struct {
	Panicked bool   `json:"panicked"`
	Affected int    `json:"affected"`
	Reason   string `json:"reason,omitempty"`
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

// WorkspaceReadyResponse reports whether an island's repo clone has landed in
// /workspace yet (GET /v1/islands/:name/workspace-ready). `dejima connect` polls
// it to avoid attaching into a still-provisioning, empty workspace.
type WorkspaceReadyResponse struct {
	Ready bool `json:"ready"`
}

// PutGitHubIdentityRequest is the body of PUT /v1/credentials/github/:name —
// how a client (e.g. `dejima auth push --github`) seeds or updates an identity.
type PutGitHubIdentityRequest struct {
	Login   string `json:"login"`
	ID      int64  `json:"id,omitempty"`   // GitHub numeric user id, for the canonical noreply commit email
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

// ProviderCredentialsResponse is the body of GET /v1/credentials/providers: the
// daemon's LLM provider credentials WITHOUT their keys (providercreds.Meta
// carries only a masked hint).
type ProviderCredentialsResponse struct {
	Providers []providercreds.Meta `json:"providers"`
}

// PutProviderCredentialRequest is the body of PUT /v1/credentials/providers/:provider
// — how a client (`dejima provider set`) seeds or updates a provider key. The
// key is write-only; it is never echoed back.
type PutProviderCredentialRequest struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url,omitempty"` // optional endpoint override
	EnvVar  string `json:"env_var,omitempty"`  // override the derived env-var name
	Default bool   `json:"default,omitempty"`  // make this the default provider
}

// DeleteProviderCredentialResponse reports which islands still reference the
// provider that was just deleted (their agents will read missing-provider-auth
// until reconfigured or pointed at another provider).
type DeleteProviderCredentialResponse struct {
	AffectedIslands []string `json:"affected_islands,omitempty"`
}

// AgentConfigRequest is the body of PATCH /v1/islands/:name/agents/:id/config.
// Pointer fields distinguish "leave unchanged" (nil) from an explicit value
// (incl. "" to clear).
type AgentConfigRequest struct {
	Provider *string `json:"provider,omitempty"`
	Model    *string `json:"model,omitempty"`
}

// AgentConfigResponse echoes the agent's resulting provider/model and whether
// the change needs a container recreate to take effect (a Model change rides an
// immutable env var; a Provider/key change re-materializes on the next restart).
type AgentConfigResponse struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	RestartRequired bool   `json:"restart_required"`
}

// AgentTypeCapability describes what a built-in agent type supports — drives the
// provider/model picker and the channels affordance in clients.
type AgentTypeCapability struct {
	Type                string   `json:"type"`
	Interactive         bool     `json:"interactive"`
	RequiresProviderKey bool     `json:"requires_provider_key"`
	SupportedProviders  []string `json:"supported_providers,omitempty"`
	SuggestedModels     []string `json:"suggested_models,omitempty"`
	GatewayPort         int      `json:"gateway_port,omitempty"` // 0 = no localhost UI to open
}

// AgentTypesResponse is the body of GET /v1/agent-types.
type AgentTypesResponse struct {
	Types []AgentTypeCapability `json:"types"`
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

// CapabilityGrantRequest is the body of POST /v1/islands/:name/capability/grants
// — grant the island permission to invoke a named host capability target.
type CapabilityGrantRequest struct {
	Target string `json:"target"`
}

// CapabilityGrantView is one capability grant as returned by the API.
type CapabilityGrantView struct {
	Target    string    `json:"target"`
	GrantedAt time.Time `json:"granted_at"`
}

// CapabilityGrantsResponse is the body of GET /v1/islands/:name/capability/grants.
type CapabilityGrantsResponse struct {
	Grants []CapabilityGrantView `json:"grants"`
}

// CapabilityExecuteRequest is the body of POST /v1/capabilities/execute. Island
// is supplied by an operator caller; a token-authenticated in-island caller is
// pinned to its own island by its bearer token and Island is ignored.
type CapabilityExecuteRequest struct {
	Island string            `json:"island,omitempty"`
	Target string            `json:"target"`
	Args   map[string]string `json:"args,omitempty"`
}

// CapabilityExecuteResponse is the result of a capability invocation.
type CapabilityExecuteResponse struct {
	OK        bool   `json:"ok"`
	Output    string `json:"output,omitempty"`
	ExitCode  int    `json:"exit_code"`
	LedgerSeq uint64 `json:"ledger_seq,omitempty"`
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

// AuditResponse is the body of GET /v1/audit — the Ledger (brokered-operation
// records plus, when the operational audit log is enabled, api.request and
// lifecycle records), filtered per the request, plus the result of verifying
// the hash chain. Verification always runs over the WHOLE chain regardless of
// any filter or limit: tamper-evidence covers the complete file, not the slice
// the caller asked to see.
type AuditResponse struct {
	Entries  []ledger.Entry `json:"entries"`
	Total    int            `json:"total"`           // entries in the whole ledger (before filtering)
	Returned int            `json:"returned"`        // entries returned after filter + limit
	Verified bool           `json:"verified"`        // whole-chain hash verification result
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
