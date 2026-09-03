package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/aoos/dejima/internal/runtime"
)

// bridgeGateway is a seam, and it is load-bearing for the TESTS rather than for
// the daemon.
//
// Without it, every assertion in hostinternal_test.go passes for the same wrong
// reason: this environment has no docker, so the lookup always fails and the
// function always returns its input unchanged. "Explicit is never overridden"
// then holds because NOTHING is ever overridden — and the mutation proving it
// (deleting the explicit check) SURVIVED, which is how I found out. A guard
// whose subject can never fire is not a guard.
var bridgeGateway = func(ctx context.Context) (netip.Addr, error) {
	return runtime.NewDocker().BridgeGateway(ctx)
}

// Choosing where the island-facing listeners bind.
//
// Islands reach the daemon's two host-internal listeners — the token-
// authenticated autonomy API and the egress proxy — by name: every container
// gets `--add-host host.docker.internal:host-gateway`. THAT NAME IS THE BRIDGE
// GATEWAY, not the host's loopback.
//
// On Docker Desktop and colima the engine runs in a VM that special-cases the
// name to reach the host's loopback through it, so a 127.0.0.1 bind works. A
// NATIVE ENGINE — plain dockerd, which is what get.docker.com installs — has no
// VM and no indirection, so a loopback bind is simply not at the address the
// container dials. Both listeners were unreachable from every island on every
// native engine, and nobody noticed because every host we develop on has a VM.
//
// THE CHOICE IS SELF-DETECTING RATHER THAN CLASSIFIED, which is the part worth
// keeping. There is no clean test for "is this engine one of the VM ones" —
// Docker Desktop for Linux, colima and plain dockerd all differ, and guessing
// wrong breaks containers in one direction or needlessly widens a bind in the
// other. But the bind itself answers the question: on a VM engine the gateway
// address belongs to the VM and does not exist on the host, so binding it FAILS.
// On a native engine it succeeds. So we try, and fall back.
//
// An EXPLICIT address is never overridden. The operator setting --egress-proxy
// or DEJIMAD_EGRESS_PROXY has made a decision, possibly the manual workaround
// for this very bug, and silently moving their listener would be worse than the
// bug.
//
// This never widens to a wildcard: the gateway is one specific host-internal
// address, reachable from containers on that bridge and from the host, not from
// the LAN. assertHostInternalBind still rejects 0.0.0.0 and still runs.
func hostInternalBind(ctx context.Context, log *slog.Logger, addr string, explicit bool) string {
	if explicit {
		return addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	// Only a loopback default is a candidate for relocation. Anything else was
	// already deliberate.
	if ip, perr := netip.ParseAddr(host); perr != nil || !ip.IsLoopback() {
		return addr
	}

	gwCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	gw, err := bridgeGateway(gwCtx)
	if err != nil {
		// No engine, no bridge, or docker not answering. Keep the default: this
		// runs at startup and must never be the reason the daemon does not come up.
		log.Debug("bridge gateway not available; island listeners stay on loopback",
			"addr", addr, "err", err)
		return addr
	}

	candidate := net.JoinHostPort(gw.String(), port)
	// The bind IS the detection. Probe it, then hand the address back rather
	// than the socket, so the caller keeps its own listen/fatal semantics — the
	// egress and token paths degrade differently and this must not flatten that.
	probe, err := net.Listen("tcp", candidate)
	if err != nil {
		log.Debug("bridge gateway is not bindable here (expected on VM-backed engines); staying on loopback",
			"gateway", candidate, "err", err)
		return addr
	}
	_ = probe.Close()

	log.Info("island listeners bind the docker bridge gateway — a container cannot reach the host's loopback on a native engine",
		"from", addr, "to", candidate)
	return candidate
}
