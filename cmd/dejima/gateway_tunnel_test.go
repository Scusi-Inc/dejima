package main

import (
	"fmt"
	"net"
	"os/exec"
	"testing"
)

// liveForward returns a gatewayForward wrapping a real, long-running process and
// a real listener on its port, so Alive() and the gateway probe both see the
// truth rather than a stub. Cleaned up by the test.
func liveForward(t *testing.T, island, agent string) (*gatewayForward, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Answer one byte to anything that connects, so gatewayReady sees a serving
	// far end. Without this, Get() correctly reaps the tunnel as dead and every
	// reuse assertion below would pass for the wrong reason.
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\n\r\n"))
			_ = c.Close()
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := 0
	if _, err := fmt.Sscan(portStr, &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process here: %v", err)
	}
	fwd := &gatewayForward{
		Island: island, AgentID: agent, LocalPort: port,
		URL: "http://localhost:" + portStr + "/",
		cmd: cmd, exited: make(chan struct{}),
	}
	go func() { fwd.waitErr = cmd.Wait(); close(fwd.exited) }()
	t.Cleanup(func() { fwd.Close(); ln.Close() })
	return fwd, ln
}

// Re-opening an agent that already has a live forward must REUSE it. A second
// ssh would bind a second port, and the browser tab the operator already has
// open points at the first — so "opening it again" would strand the tab it was
// meant to fix.
func TestTunnelManager_ReusesALiveForward(t *testing.T) {
	m := newTunnelManager()
	fwd, _ := liveForward(t, "isl", "a1")
	m.Put(fwd)

	got := m.Get("isl", "a1")
	if got == nil {
		t.Fatal("a live forward with a serving gateway was not returned")
	}
	if got != fwd {
		t.Error("a different forward came back; the registry is not keyed as expected")
	}
}

// A LIVE ssh IS NOT A LIVE GATEWAY. ssh -N -L stays up happily while the far end
// is gone — restart the agent and the tunnel is healthy and the gateway is not.
// This matters most on the reuse path, because reuse SKIPS the spawn: without a
// probe, the dead case would be the one that never recovers.
func TestTunnelManager_ReapsALiveTunnelToADeadGateway(t *testing.T) {
	m := newTunnelManager()
	fwd, ln := liveForward(t, "isl", "a1")
	m.Put(fwd)

	// The gateway goes away; ssh keeps running, exactly as it would in the field.
	ln.Close()

	if got := m.Get("isl", "a1"); got != nil {
		t.Fatal("a tunnel to a dead gateway was reused — the browser would load " +
			"nothing and this is the one path that never retries")
	}
	if !fwd.Alive() == false && fwd.Alive() {
		t.Error("the reaped forward's process was left running")
	}
}

// A forward whose ssh has exited must not be handed back; the port is closed and
// the browser would land on nothing.
func TestTunnelManager_ReapsADeadProcess(t *testing.T) {
	m := newTunnelManager()
	fwd, _ := liveForward(t, "isl", "a1")
	m.Put(fwd)
	fwd.Close() // ssh dies

	if got := m.Get("isl", "a1"); got != nil {
		t.Fatal("a forward whose process had exited was returned")
	}
}

// THE PORT MUST SURVIVE THE FORWARD. Reuse only helps while a tunnel is alive;
// the case the operator hit is a tunnel that DIED. If re-establishing picks a
// fresh port, the tab they already have open stays broken — they reload, get the
// same error, and reasonably conclude nothing was fixed.
func TestTunnelManager_RemembersThePortAfterTheForwardDies(t *testing.T) {
	m := newTunnelManager()
	fwd, _ := liveForward(t, "isl", "a1")
	want := fwd.LocalPort
	m.Put(fwd)

	fwd.Close()
	if got := m.Get("isl", "a1"); got != nil {
		t.Fatal("dead forward was not reaped")
	}
	if got := m.portFor("isl", "a1"); got != want {
		t.Errorf("portFor = %d, want %d — a re-established forward would pick a new "+
			"port and leave the operator's open tab pointing at nothing", got, want)
	}
}

// CloseAll is the dashboard's teardown. Every forward must die and the registry
// must empty, or a second dashboard run inherits handles to processes it does
// not own.
func TestTunnelManager_CloseAllKillsEverything(t *testing.T) {
	m := newTunnelManager()
	a, _ := liveForward(t, "isl", "a1")
	b, _ := liveForward(t, "isl", "a2")
	m.Put(a)
	m.Put(b)
	if m.Count() != 2 {
		t.Fatalf("Count = %d, want 2", m.Count())
	}

	m.CloseAll()
	if a.Alive() || b.Alive() {
		t.Error("a forward survived CloseAll")
	}
	if m.Count() != 0 {
		t.Errorf("Count = %d after CloseAll, want 0", m.Count())
	}
}

// Putting a second forward for the same agent closes the first, so two ssh
// processes can never hold two ports for one gateway.
func TestTunnelManager_PutReplacesAndClosesThePrevious(t *testing.T) {
	m := newTunnelManager()
	first, _ := liveForward(t, "isl", "a1")
	m.Put(first)
	second, _ := liveForward(t, "isl", "a1")
	m.Put(second)

	if first.Alive() {
		t.Error("the previous forward for this agent was left running")
	}
	if m.Count() != 1 {
		t.Errorf("Count = %d, want 1 — one agent should hold one forward", m.Count())
	}
}

// THE REASON THIS TYPE EXISTS. runTUI runs bubbletea in a loop: the program exits
// to run an attach session, then re-enters with a FRESHLY CONSTRUCTED model. A
// registry living on tuiModel would be discarded on every attach, so attaching to
// any agent would silently kill an open gateway UI.
//
// This simulates that: build a model, hand it the manager, discard the model,
// build another — the forward must still be there.
func TestTunnelManager_SurvivesADashboardReEntry(t *testing.T) {
	m := newTunnelManager()
	fwd, _ := liveForward(t, "isl", "a1")
	m.Put(fwd)

	first := tuiModel{tunnels: m}
	_ = first // the model goes out of scope, as it does on every attach
	second := tuiModel{tunnels: m}

	if second.tunnels.Get("isl", "a1") == nil {
		t.Fatal("the forward did not survive a dashboard re-entry — attaching to any " +
			"agent would silently kill an open gateway UI, which is the same surprise " +
			"as the closed window this change removes")
	}
}
