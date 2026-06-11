package api

import "time"

// IslandInfo is the public view of an island returned by the API.
type IslandInfo struct {
	Name string `json:"name"`
	Repo string `json:"repo"`
	// Agent is the island's agent type (e.g. "claude-code", "codex", "headless").
	Agent string `json:"agent"`
	Image string `json:"image"`
	// Cmd is the user-supplied entrypoint for headless islands; empty for
	// the built-in CLI agents.
	Cmd        string          `json:"cmd,omitempty"`
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
	// State is the agent's session liveness ("running"/"stopped"/""); populated
	// from Phase 2 onward.
	State      string          `json:"state,omitempty"`
	AgentState *AgentStateInfo `json:"agent_state,omitempty"`
	Attached   []PresenceEntry `json:"attached,omitempty"`
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
	// DaemonVersion / APIVersion let a client detect skew against the daemon.
	// APIVersion is 0 from daemons predating version reporting.
	DaemonVersion string `json:"daemon_version,omitempty"`
	APIVersion    int    `json:"api_version,omitempty"`
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
	// Agents, when non-empty, seeds the island with multiple agents. When empty,
	// the scalar Agent/Cmd above describe a single agent (back-compat). Consumed
	// from Phase 2 onward; accepted-but-unused before then.
	Agents []AgentSpecRequest `json:"agents,omitempty"`
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

// ErrorResponse is the body of any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}
