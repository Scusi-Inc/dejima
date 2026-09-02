package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// readSource returns a file from this package, for the ordering guard below.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// An EXPLICIT address is never relocated. The operator who set --egress-proxy or
// DEJIMAD_EGRESS_PROXY has made a decision — possibly the manual workaround for
// this exact bug — and silently moving their listener would be worse than the
// bug it fixes.
func TestHostInternalBind_NeverOverridesExplicit(t *testing.T) {
	// A gateway that is bindable AND NOT LOOPBACK, so relocation would visibly
	// change the address if the explicit check were absent. Stubbing loopback here
	// is not enough: the candidate then equals the input and the mutation deleting
	// the explicit check survives anyway. It did, twice, until this line.
	withStubGateway(t, bindableNonLoopback(t))

	for _, addr := range []string{"127.0.0.1:7280", "172.17.0.1:7280", "10.0.0.5:7274"} {
		if got := hostInternalBind(context.Background(), quietLog(), addr, true); got != addr {
			t.Errorf("explicit %q was relocated to %q", addr, got)
		}
	}
}

// withStubGateway makes the engine lookup return a fixed address for one test.
func withStubGateway(t *testing.T, addr netip.Addr) {
	t.Helper()
	prev := bridgeGateway
	bridgeGateway = func(context.Context) (netip.Addr, error) { return addr, nil }
	t.Cleanup(func() { bridgeGateway = prev })
}

// bindableNonLoopback finds an address on this machine that is not loopback and
// that a listener can actually bind — standing in for a real bridge gateway,
// which is exactly that shape. Discovered rather than hardcoded: the right
// address differs per machine and a literal would make this pass or skip for
// reasons unrelated to the code.
func bindableNonLoopback(t *testing.T) netip.Addr {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		ln, lerr := net.Listen("tcp", net.JoinHostPort(ipn.IP.String(), "0"))
		if lerr != nil {
			continue
		}
		ln.Close()
		got, _ := netip.AddrFromSlice(ipn.IP.To4())
		return got
	}
	t.Skip("no bindable non-loopback address here; the relocation control cannot run")
	return netip.Addr{}
}

// THE POSITIVE CONTROL FOR EVERY "unchanged" ASSERTION IN THIS FILE.
//
// They all assert the address comes back untouched — which is also what a
// completely broken function returns. This proves the relocation path CAN fire,
// so an unchanged result elsewhere means the condition was not met rather than
// that nothing works.
//
// It is not decoration: the mutation deleting the explicit-override check
// SURVIVED until this existed, because with no docker here the function returned
// unchanged for a reason that had nothing to do with the check under test.
func TestHostInternalBind_RelocatesWhenTheGatewayIsBindable(t *testing.T) {
	gw := bindableNonLoopback(t)
	withStubGateway(t, gw)

	got := hostInternalBind(context.Background(), quietLog(), "127.0.0.1:7280", false)
	want := net.JoinHostPort(gw.String(), "7280")
	if got != want {
		t.Fatalf("got %q, want %q — the relocation path did not fire with a bindable "+
			"gateway, which makes every fallback assertion in this file vacuous", got, want)
	}
}

// An UNBINDABLE gateway must fall back, and this is the case that matters most:
// it is what every VM-backed engine looks like. Docker Desktop reports a gateway
// belonging to the VM, which does not exist on the host — so the probe is the
// only thing standing between a correct fallback and a daemon that binds nothing
// and reports success.
//
// 192.0.2.0/24 is TEST-NET-1: reserved for documentation and guaranteed not
// configured on any real interface, so this is deterministic rather than
// dependent on what this machine happens to have.
func TestHostInternalBind_FallsBackWhenTheGatewayIsNotBindable(t *testing.T) {
	withStubGateway(t, netip.MustParseAddr("192.0.2.1"))

	const addr = "127.0.0.1:7280"
	got := hostInternalBind(context.Background(), quietLog(), addr, false)
	if got != addr {
		t.Fatalf("got %q, want the unchanged default %q — an unbindable gateway was "+
			"accepted, so on a VM-backed engine the daemon would try to listen on an "+
			"address the host does not have", got, addr)
	}
}

// Only a LOOPBACK default is a candidate. Anything else was already a deliberate
// choice by someone, even if they did not pass a flag this run.
func TestHostInternalBind_OnlyRelocatesLoopback(t *testing.T) {
	// Stubbed for the same reason as the explicit test: without it this passes
	// because there is no docker here, and the mutation removing the loopback
	// restriction survives. THIS IS THE THIRD TEST IN THIS FILE THAT NEEDED IT —
	// every "returns unchanged" assertion is vacuous by default, because
	// unchanged is also what the function does when the engine cannot be asked.
	withStubGateway(t, bindableNonLoopback(t))

	for _, addr := range []string{"172.17.0.1:7280", "192.168.1.5:7274", "example.internal:7280"} {
		if got := hostInternalBind(context.Background(), quietLog(), addr, false); got != addr {
			t.Errorf("non-loopback default %q was relocated to %q", addr, got)
		}
	}
}

// With no engine reachable — which is this test environment, and also a host
// where docker is not installed or not running — the default must survive
// untouched. This runs at daemon startup and must never be the reason it does
// not come up.
func TestHostInternalBind_FallsBackWhenNoEngine(t *testing.T) {
	const addr = "127.0.0.1:7280"
	got := hostInternalBind(context.Background(), quietLog(), addr, false)
	if got != addr {
		t.Fatalf("got %q, want the unchanged default %q — with no docker to ask, "+
			"relocation must not happen and must not fail startup", got, addr)
	}
}

// A malformed address is returned unchanged rather than being parsed into
// something else. The caller's own validation reports it, with its own message.
func TestHostInternalBind_LeavesMalformedAlone(t *testing.T) {
	withStubGateway(t, bindableNonLoopback(t)) // else this passes for the env's reason too

	for _, addr := range []string{"", "not-an-addr", "127.0.0.1"} {
		if got := hostInternalBind(context.Background(), quietLog(), addr, false); got != addr {
			t.Errorf("malformed %q was rewritten to %q", addr, got)
		}
	}
}

// THE GUARD MUST STILL SEE WHAT WE ACTUALLY BIND. Relocation happens BEFORE
// assertHostInternalBind in both paths; if it moved after, the relocated address
// would be unguarded — which is how a wildcard eventually slips past a check
// that only ever inspected the default.
func TestRelocationHappensBeforeTheWildcardGuard(t *testing.T) {
	src := readSource(t, "main.go")
	for _, c := range []struct{ addr, guardArg string }{
		{"tokenAddr", "assertHostInternalBind(log, tokenAddr)"},
		{"egressAddr", "assertHostInternalBind(log, egressAddr)"},
	} {
		reloc := strings.Index(src, c.addr+" = hostInternalBind(")
		guard := strings.Index(src, c.guardArg)
		if reloc < 0 {
			t.Errorf("%s is never relocated — the listener stays on loopback and is "+
				"unreachable from containers on a native engine", c.addr)
			continue
		}
		if guard < 0 {
			t.Errorf("no assertHostInternalBind call found for %s — this guard's "+
				"premise has moved", c.addr)
			continue
		}
		if reloc > guard {
			t.Errorf("%s is relocated AFTER the wildcard guard runs, so the address "+
				"actually bound is never validated", c.addr)
		}
	}
}

// The wildcard refusal is untouched by this change and must stay that way: it is
// the reason a bridge-gateway bind is acceptable at all. Binding one specific
// host-internal address is not the same as binding everything.
func TestWildcardBindStillRefused(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7280", ":7280", "[::]:7280"} {
		if err := assertHostInternalBind(quietLog(), addr); err == nil {
			t.Errorf("wildcard %q was accepted", addr)
		}
	}
	// And a bridge gateway is still ALLOWED — the guard's own comment says so,
	// and the fix depends on it.
	if err := assertHostInternalBind(quietLog(), "172.17.0.1:7280"); err != nil {
		t.Errorf("a bridge gateway was refused (%v); the fix relies on this being allowed", err)
	}
}
