// Package runtime defines the backend abstraction for container runtimes.
//
// v1 ships a Docker backend. Future backends (Podman, Apple `container`,
// Firecracker, remote-SSH) can implement the same interface without
// changing the daemon, API, or CLI.
package runtime
