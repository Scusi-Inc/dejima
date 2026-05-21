package api

import "time"

// IslandInfo is the public view of an island returned by the API.
type IslandInfo struct {
	Name       string          `json:"name"`
	Repo       string          `json:"repo"`
	Agent      string          `json:"agent"`
	Image      string          `json:"image"`
	State      string          `json:"state"`     // desired state from config
	Container  string          `json:"container"` // observed status from runtime
	CreatedAt  time.Time       `json:"created_at"`
	LastUsedAt time.Time       `json:"last_used_at"`
	Attached   []PresenceEntry `json:"attached,omitempty"`
	Stats      *IslandStats    `json:"stats,omitempty"`
}

// IslandStats is a snapshot of the container's resource usage.
type IslandStats struct {
	MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
	CPUPercent       float64 `json:"cpu_percent"`
}

// CreateIslandRequest is the body of POST /v1/islands.
type CreateIslandRequest struct {
	Name      string    `json:"name,omitempty"`  // optional; derived from repo if empty
	Repo      string    `json:"repo"`            // required
	Agent     string    `json:"agent,omitempty"` // defaults to "claude-code"
	Image     string    `json:"image,omitempty"` // defaults to "dejima/island:latest"
	Resources Resources `json:"resources,omitempty"`
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

// ErrorResponse is the body of any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}
