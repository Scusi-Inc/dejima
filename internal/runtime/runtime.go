package runtime

import (
	"context"
	"io"
)

// ContainerStatus is a coarse-grained state from the runtime's perspective.
type ContainerStatus string

const (
	StatusMissing ContainerStatus = "missing"
	StatusCreated ContainerStatus = "created"
	StatusRunning ContainerStatus = "running"
	StatusStopped ContainerStatus = "stopped"
	StatusExited  ContainerStatus = "exited"
	StatusErrored ContainerStatus = "errored"
)

// CreateRequest describes a container to be created.
type CreateRequest struct {
	Name        string
	Image       string
	Env         map[string]string
	Volumes     []VolumeMount // named volumes
	BindMounts  []BindMount   // host path → container path (read-only by default in M1)
	Command     []string      // override entrypoint command (optional)
	Labels      map[string]string
	Memory      string // e.g. "4G" → --memory
	CPUs        string // e.g. "2.0" → --cpus
	StorageSize string // e.g. "20G" → --storage-opt size=
	Network     string // user-defined bridge network name (empty = default)
	// ExtraHosts are "host:ip" entries added with --add-host. Used to give the
	// container a route to the daemon's host-internal listener (the in-island
	// autonomy/telemetry path), e.g. "host.docker.internal:host-gateway".
	ExtraHosts []string
}

// VolumeMount represents a named-volume mount.
type VolumeMount struct {
	Name   string
	Target string
}

// BindMount represents a host-directory bind mount.
type BindMount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// Stats holds a snapshot of a container's resource usage.
type Stats struct {
	MemoryUsageBytes uint64
	MemoryLimitBytes uint64
	CPUPercent       float64
}

// Health holds crash-relevant facts from a container inspect. These can't be
// derived by a remote client (they require engine access), so the daemon
// surfaces them for monitoring/dashboards.
type Health struct {
	OOMKilled    bool // last run was killed by the OOM killer (hit its memory cap)
	RestartCount int  // cumulative restarts under the restart policy
	ExitCode     int  // last exit code (0 if running or never exited)
}

// Runtime is the backend abstraction over a container engine.
type Runtime interface {
	// EnsureVolume creates a volume if it doesn't exist. Idempotent.
	EnsureVolume(ctx context.Context, name string) error

	// RemoveVolume deletes a volume. Errors if missing unless force=true.
	RemoveVolume(ctx context.Context, name string, force bool) error

	// EnsureNetwork creates a user-defined bridge network if it doesn't exist.
	// Idempotent. The network isolates containers from other networks while
	// retaining outbound internet access via Docker's NAT.
	EnsureNetwork(ctx context.Context, name string) error

	// RemoveNetwork deletes a network. Tolerates missing.
	RemoveNetwork(ctx context.Context, name string) error

	// Stats returns the container's current resource usage. Returns zero-valued
	// stats if the container is not running or stats are unavailable.
	Stats(ctx context.Context, name string) (Stats, error)

	// StatsAll returns current resource usage for every running container in
	// one engine query, keyed by container name. One `docker stats` sampling
	// interval (~2s) covers any number of containers — callers serving lists
	// must use this instead of per-container Stats calls.
	StatsAll(ctx context.Context) (map[string]Stats, error)

	// VolumeSizes returns the on-disk size in bytes of every volume in one
	// engine query, keyed by volume name. Slower than StatsAll and reads 0 on
	// storage drivers that don't report volume size, so callers should poll it
	// sparingly and treat 0 as "unknown".
	VolumeSizes(ctx context.Context) (map[string]int64, error)

	// CreateContainer creates and starts a container. Returns the container ID.
	CreateContainer(ctx context.Context, req CreateRequest) (string, error)

	// StopContainer gracefully stops a running container.
	StopContainer(ctx context.Context, name string) error

	// StartContainer starts a stopped container.
	StartContainer(ctx context.Context, name string) error

	// RemoveContainer removes a container (must be stopped unless force=true).
	RemoveContainer(ctx context.Context, name string, force bool) error

	// Status returns the container's current status.
	Status(ctx context.Context, name string) (ContainerStatus, error)

	// Inspect returns crash-relevant health facts (OOM, restarts, exit code).
	// Returns a zero Health if the container is missing or unavailable.
	Inspect(ctx context.Context, name string) (Health, error)

	// Exec runs a command inside a running container, returning stdout/stderr and exit code.
	Exec(ctx context.Context, name string, cmd []string) (stdout, stderr string, exitCode int, err error)

	// ExecStream runs a command inside a running container and streams its
	// combined stdout/stderr until the command exits or ctx is canceled. Used
	// for following a per-agent log file (`tail -f`).
	ExecStream(ctx context.Context, name string, cmd []string) (io.ReadCloser, error)

	// ImageExists reports whether the runtime has the named image locally.
	ImageExists(ctx context.Context, image string) (bool, error)

	// BuildImage builds tag from the build context at contextDir (dockerfile
	// is relative to it), streaming combined build output. A failed build
	// surfaces as a non-EOF error from the stream's final Read.
	BuildImage(ctx context.Context, contextDir, dockerfile, tag string) (io.ReadCloser, error)

	// CopyToContainer copies a file or directory from host to container path.
	CopyToContainer(ctx context.Context, name, hostPath, containerPath string) error

	// CopyFromContainer copies a file or directory from container to host path.
	CopyFromContainer(ctx context.Context, name, containerPath, hostPath string) error

	// Logs returns the container's accumulated stdout/stderr. If follow is true,
	// the reader streams new output until ctx is canceled.
	Logs(ctx context.Context, name string, follow bool) (io.ReadCloser, error)
}
