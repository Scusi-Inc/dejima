// Package bridge implements the PTY multiplexing between the in-container
// tmux session and N connected API clients.
//
// In v1 the bridge is a thin wrapper over container primitives: tmux inside,
// docker exec / API for PTY plumbing outside. The public API contract is the
// stable surface; the bridge implementation is replaceable.
package bridge
