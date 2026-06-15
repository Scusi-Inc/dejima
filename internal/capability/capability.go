// Package capability runs curated host actions on behalf of a contained island
// — the execution half of the capability broker (docs/capability-broker-spec.md).
// An Adapter maps a (target, args) request to a host action without ever
// reaching a shell. Who may invoke what (grants) lives in internal/project; this
// package is only ever reached after a grant check.
package capability

import (
	"context"
	"errors"
)

// Sentinel errors let the API layer map adapter failures to HTTP status codes
// without leaking host detail. A target that *ran* and exited non-zero is NOT an
// error — it returns a Result with a non-zero ExitCode and a nil error.
var (
	ErrTargetNotFound  = errors.New("capability target not found")
	ErrTargetUntrusted = errors.New("capability target failed its trust checks")
	ErrTimeout         = errors.New("capability execution timed out")
)

// Request is one capability invocation.
type Request struct {
	Island string
	Agent  string
	Target string
	Args   map[string]string
}

// Result is the outcome of a target that started. Output is captured stdout+
// stderr (bounded); ExitCode is the process exit status.
type Result struct {
	Output   string
	ExitCode int
}

// Adapter maps a (target, args) request to a host action. Implementations MUST
// never construct a shell command from request data: exec a fixed program with a
// fixed argv and pass args out-of-band (JSON on stdin). Name identifies the
// adapter for the Ledger ("script" | "shortcuts").
type Adapter interface {
	Name() string
	Execute(ctx context.Context, req Request) (Result, error)
}
