package mcpbroker

import (
	"context"
	"time"
)

const (
	defaultCallTimeout = 30 * time.Second
	defaultMaxOutput   = 1 << 20 // 1 MiB captured JSON-RPC result
)

// StdioBroker invokes stdio MCP servers named in a host-side Registry. It is the
// V1 broker; a future transport (e.g. HTTP/SSE) is a sibling implementation
// selected the same way, never a generalization of this one.
type StdioBroker struct {
	Registry  *Registry
	Timeout   time.Duration // 0 → defaultCallTimeout
	MaxOutput int64         // 0 → defaultMaxOutput
}

// Default returns the broker backed by ~/.dejima/mcp/servers.toml.
func Default() (Broker, error) {
	reg, err := DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return &StdioBroker{Registry: reg}, nil
}

func (b *StdioBroker) Name() string { return "stdio" }

// Call resolves the named server in the registry (deny-closed if absent or the
// registry is untrusted), enforces the brokered method surface, and performs one
// JSON-RPC round-trip bounded by the timeout and output cap. The grant check
// (deny-all) happens upstream in the API layer; this is only ever reached after
// it passes.
func (b *StdioBroker) Call(ctx context.Context, req Request) (Result, error) {
	if !MethodAllowed(req.Method) {
		return Result{}, ErrMethodNotAllowed
	}
	spec, err := b.Registry.Lookup(req.Server)
	if err != nil {
		return Result{}, err
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	max := b.MaxOutput
	if max <= 0 {
		max = defaultMaxOutput
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return callStdio(cctx, spec, req, max)
}
