package main

import (
	"context"
	"sync"

	"github.com/aoos/dejima/internal/api"
)

// Who owns a gateway forward, and for how long.
//
// It used to be a SPAWNED WINDOW. The dashboard ran `dejima agent open` in a new
// terminal, that command held the ssh forward for its lifetime, and closing the
// window killed the tunnel under an already-open browser tab —
// ERR_CONNECTION_REFUSED, with nothing on the page or in the dashboard saying
// why. Correct behaviour, and a window nobody knew was load-bearing.
//
// Now the dashboard holds it, and the lifetime is one the operator can hold in
// their head: THE UI WORKS FOR AS LONG AS THE DASHBOARD IS OPEN.
//
// IT DOES NOT LIVE ON tuiModel, AND THAT IS THE WHOLE REASON THIS TYPE EXISTS.
// runTUI runs bubbletea in a LOOP: the program exits to run an attach session and
// then re-enters with a freshly constructed model. A registry on the model would
// be discarded on every attach, so attaching to any agent would silently kill the
// gateway UI — the same surprise as the closed window, in a new place. This is
// owned by runTUI's scope and survives program restarts.
type tunnelManager struct {
	mu sync.Mutex
	// keyed by island/agent, so re-opening the same agent finds its live forward
	// instead of starting a second ssh onto a second port.
	tunnels map[string]*gatewayForward
	// lastPort remembers the port each agent was last forwarded on, SEPARATELY
	// from the live handle, and outlives it.
	//
	// d1's catch, and it is the case the operator actually hit: reuse only helps
	// while a tunnel is alive. When one DIES and is re-established, a fresh
	// free port leaves the tab they already have open pointed at the old one.
	// They reload, get ERR_CONNECTION_REFUSED again, and conclude it is still
	// broken — because from where they are sitting, it is.
	//
	// Preferring the previous port means a stale tab recovers on reload, which is
	// exactly what a person does when a page fails.
	lastPort map[string]int
}

func newTunnelManager() *tunnelManager {
	return &tunnelManager{
		tunnels:  map[string]*gatewayForward{},
		lastPort: map[string]int{},
	}
}

func tunnelKey(island, agentID string) string { return island + "/" + agentID }

// Get returns a LIVE forward for the agent, or nil.
//
// A forward whose ssh has exited is reaped here rather than returned. Handing
// back a dead one would point the browser at a closed port and look like the
// original bug — and the caller cannot tell the difference from the outside,
// which is why the check belongs here and not at each call site.
func (m *tunnelManager) Get(island, agentID string) *gatewayForward {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := tunnelKey(island, agentID)
	fwd, ok := m.tunnels[k]
	if !ok {
		return nil
	}
	if !fwd.Alive() {
		delete(m.tunnels, k)
		return nil
	}
	// A LIVE ssh IS NOT A LIVE GATEWAY. `ssh -N -L` stays up perfectly happily
	// while the thing at the far end is gone — restart the agent, or let the
	// framework crash, and the tunnel is healthy and the gateway is not.
	//
	// This matters more here than anywhere else: reuse SKIPS the spawn, so
	// without a probe the dead case becomes PERMANENT. The one path that never
	// recovers would be the one that reuses. (d1's catch; same shape as calling a
	// running process an answering service.)
	if !gatewayReady(context.Background(), fwd.LocalPort) {
		fwd.Close()
		delete(m.tunnels, k)
		return nil
	}
	return fwd
}

// Put stores a live forward, closing any previous one for the same agent so two
// ssh processes can never hold two ports for one gateway.
func (m *tunnelManager) Put(fwd *gatewayForward) {
	if fwd == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := tunnelKey(fwd.Island, fwd.AgentID)
	if prev, ok := m.tunnels[k]; ok && prev != fwd {
		prev.Close()
	}
	m.tunnels[k] = fwd
	m.lastPort[k] = fwd.LocalPort
}

// portFor returns the port this agent last used, or 0. Kept after the forward
// dies so a re-establish can land on the same port and rescue a stale tab.
func (m *tunnelManager) portFor(island, agentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPort[tunnelKey(island, agentID)]
}

// Count reports how many forwards are live, reaping dead ones on the way.
// The dashboard uses it to say what quitting will close.
func (m *tunnelManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, fwd := range m.tunnels {
		if !fwd.Alive() {
			delete(m.tunnels, k)
		}
	}
	return len(m.tunnels)
}

// CloseAll terminates every forward. Called when the dashboard exits for good —
// deferred in runTUI, so it also covers the context-cancelled path.
func (m *tunnelManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, fwd := range m.tunnels {
		fwd.Close()
		delete(m.tunnels, k)
	}
}

// openGatewayForAgent returns a live forward for the agent, reusing an existing
// one when there is one. The caller opens fwd.URL.
func (m *tunnelManager) openGatewayForAgent(ctx context.Context, c *api.Client, island, agentRef string, notify func()) (*gatewayForward, error) {
	t, err := resolveGatewayTarget(ctx, c, island, agentRef)
	if err != nil {
		return nil, err
	}
	// Reuse before spawn: the browser tab for this agent already points at a
	// port, and a second forward would strand it on the first.
	if fwd := m.Get(t.Island, t.AgentID); fwd != nil {
		return fwd, nil
	}
	// Prefer the port this agent used last, so a browser tab left open from a
	// previous forward recovers on reload instead of staying broken.
	fwd, err := startGatewayForward(ctx, t, m.portFor(t.Island, t.AgentID), notify)
	if err != nil {
		return nil, err
	}
	m.Put(fwd)
	return fwd, nil
}
