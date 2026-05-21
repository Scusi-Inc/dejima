package runtime

import (
	"context"
	"io"
)

// ContainerStatus is a coarse-grained state from the runtime's perspective.
type ContainerStatus string

const (
	StatusMissing  ContainerStatus = "missing"
	StatusCreated  ContainerStatus = "created"
	StatusRunning  ContainerStatus = "running"
	StatusStopped  ContainerStatus = "stopped"
	StatusExited   ContainerStatus = "exited"
	StatusErrored  ContainerStatus = "errored"
)

// CreateRequest describes a container to be created.
type CreateRequest struct {
	Name        string
	Image       string
	Env         map[string]string
	Volumes     []VolumeMount    // named volumes
	BindMounts  []BindMount      // host path → container path (read-only by default in M1)
	Command     []string         // override entrypoint command (optional)
	Labels      map[string]string
	Memory      string           // e.g. "4G" → --memory
	CPUs        string           // e.g. "2.0" → --cpus
	StorageSize string           // e.g. "20G" → --storage-opt size=
	Network     string           // user-defined bridge network name (empty = default)
}

// VolumeMount represents a named-volume mount.
type VolumeMount struct {
	Name      string
	Target    string
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

	// Exec runs a command inside a running container, returning stdout/stderr and exit code.
	Exec(ctx context.Context, name string, cmd []string) (stdout, stderr string, exitCode int, err error)

	// ImageExists reports whether the runtime has the named image locally.
	ImageExists(ctx context.Context, image string) (bool, error)

	// CopyToContainer copies a file or directory from host to container path.
	CopyToContainer(ctx context.Context, name, hostPath, containerPath string) error

	// CopyFromContainer copies a file or directory from container to host path.
	CopyFromContainer(ctx context.Context, name, containerPath, hostPath string) error

	// Logs returns the container's accumulated stdout/stderr. If follow is true,
	// the reader streams new output until ctx is canceled.
	Logs(ctx context.Context, name string, follow bool) (io.ReadCloser, error)
}
