// Package mcpbroker brokers Model Context Protocol (MCP) calls on behalf of a
// contained island — the execution half of the audited MCP broker
// (docs/mcp-broker-spec.md). It mirrors the capability broker
// (internal/capability) exactly: a deny-all default, a host-side curated set of
// named MCP servers the operator authored, per-island grants of those names, a
// fixed wire contract, and every call a typed Ledger entry.
//
// A Broker maps a (server, method, params) request to one JSON-RPC call against
// a granted MCP server. It never constructs a shell command from request data:
// it execs the operator-curated server program with a fixed argv and speaks
// JSON-RPC over its stdio. Who may invoke which server (grants) lives in
// internal/project; this package is only ever reached after a grant check.
package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
)

// Sentinel errors let the API layer map broker failures to HTTP status codes
// without leaking host detail. A tools/call that *ran* and returned an MCP
// application error (isError:true) is NOT one of these — it returns a Result
// with IsError set and a nil error; only transport/protocol failures error.
var (
	// ErrServerNotFound: the named server isn't in the host registry (or the
	// registry file is absent). Fail closed — same posture as a missing grant.
	ErrServerNotFound = errors.New("mcp server not found in host registry")
	// ErrServerUntrusted: the registry file failed its trust checks (not owned
	// by the daemon user, or group/world-writable), so its contents can't be
	// trusted to name safe server programs.
	ErrServerUntrusted = errors.New("mcp server registry failed its trust checks")
	// ErrMethodNotAllowed: the requested JSON-RPC method is outside the brokered
	// surface (see AllowedMethods). The lifecycle/handshake methods are driven by
	// the broker itself and are never callable by a client.
	ErrMethodNotAllowed = errors.New("mcp method not permitted by the broker")
	// ErrTimeout: the server didn't complete the call within the wall-clock bound.
	ErrTimeout = errors.New("mcp call timed out")
	// ErrProtocol: the server spoke malformed JSON-RPC, closed early, or returned
	// a JSON-RPC error to a lifecycle/brokered request.
	ErrProtocol = errors.New("mcp protocol error")
)

// Request is one brokered MCP call. The island/agent are carried for the Ledger
// and the server's minimal environment; Server names a granted registry entry;
// Method is a member of AllowedMethods; Params is the JSON-RPC params object
// (may be nil). Nothing here reaches a shell.
type Request struct {
	Island string
	Agent  string
	Server string
	Method string
	Params json.RawMessage
}

// Result is the outcome of a brokered call that completed the JSON-RPC
// round-trip. Output is the raw JSON-RPC `result` value (bounded). IsError
// reports whether a tools/call result carried `isError: true` — an
// application-level failure that still completed the protocol, mirroring how the
// capability adapter treats a non-zero exit code: the caller's concern, not a
// broker error.
type Result struct {
	Output  json.RawMessage
	IsError bool
}

// Broker maps a Request to one JSON-RPC call against a granted, host-curated MCP
// server. Implementations MUST never build a shell command from request data:
// exec the operator-authored server program with a fixed argv and speak
// JSON-RPC over stdio. Name identifies the transport for the Ledger ("stdio").
type Broker interface {
	Name() string
	Call(ctx context.Context, req Request) (Result, error)
}

// AllowedMethods is the brokered JSON-RPC surface: discovery + invocation, never
// the lifecycle/handshake methods (initialize, notifications/*), which the
// broker owns. Keeping this a fixed allow-list is what makes the Ledger
// tractable — every entry names a published, bounded operation, exactly the
// argument that rejected a general command broker for capabilities.
var AllowedMethods = map[string]bool{
	"tools/list":     true,
	"tools/call":     true,
	"resources/list": true,
	"resources/read": true,
	"prompts/list":   true,
	"prompts/get":    true,
}

// MethodAllowed reports whether method is in the brokered surface.
func MethodAllowed(method string) bool { return AllowedMethods[method] }
