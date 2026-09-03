package main

import (
	"context"
	"fmt"
	"io"
	"net"
	neturl "net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/project"
)

// Establishing an agent's gateway forward, for BOTH callers.
//
// `dejima agent open` holds this for the life of the command; the dashboard holds
// it for the life of the dashboard. Extracted rather than reimplemented because
// the CLI path accumulated handling that a second copy would get subtly wrong and
// then drift from: the SSH-façade preflight, the provider-key warning, the
// host-key pinning, the wait for a gateway that does not exist yet during a first
// launch's `npm install`, and the token probe with its three distinct failure
// explanations.
//
// The caller decides the LIFETIME. This returns the live process and the URL; it
// never waits for the process to end.

// sshForwardArgs builds the argv for the forward, for BOTH the CLI and the
// dashboard.
//
// Shared because these flags are load-bearing and easy to lose in a copy.
// Keepalives, because this tunnel's whole job is to be LEFT OPEN: a console is
// something you open and come back to, so the traffic profile is long idle gaps —
// exactly what NAT tables and firewalls reap. Without ServerAlive, ssh sends
// nothing during those gaps, the path is silently dropped, and the browser
// reports the GATEWAY as disconnected when the gateway is fine and the tunnel
// under it is gone. ServerAliveInterval also bounds how long a dead tunnel can
// masquerade as a live one: 3 × 30s rather than until the OS notices.
//
// ExitOnForwardFailure is what makes a successful local bind mean ssh
// authenticated and accepted the forward, which waitForForward depends on.
func sshForwardArgs(khArgs []string, localPort, gatewayPort int, sshPort, island, host string) []string {
	args := []string{"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	args = append(args, khArgs...)
	return append(args,
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", localPort, gatewayPort),
		"-p", sshPort,
		island+"@"+host,
	)
}

// gatewayForward is a live `ssh -N -L` holding an agent's gateway on a local port.
type gatewayForward struct {
	Island  string
	AgentID string
	// URL is what to open — already carrying the console's auth token in its
	// fragment when the framework declares one, so no paste is needed.
	URL       string
	LocalPort int

	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
}

// Alive reports whether the ssh process is still running.
func (g *gatewayForward) Alive() bool {
	if g == nil || g.cmd == nil {
		return false
	}
	select {
	case <-g.exited:
		return false
	default:
		return true
	}
}

// Close terminates the forward. Idempotent: callers close on teardown and on
// reap, and a double close must not panic.
func (g *gatewayForward) Close() {
	if g == nil || g.cmd == nil || g.cmd.Process == nil {
		return
	}
	if !g.Alive() {
		return
	}
	_ = g.cmd.Process.Kill()
	<-g.exited
}

// portFree reports whether localhost:port can be bound right now.
//
// Inherently a race — something can take it between this check and ssh's bind —
// but the cost of losing that race is the same fallback we would have taken
// anyway, one layer later. The alternative is ssh dying on a port we chose.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// gatewayTarget is everything about an agent needed to forward its gateway,
// resolved from the daemon before any connection exists.
type gatewayTarget struct {
	Island, AgentID, AgentType string
	GatewayPort                int
	DashTokenCmd, DashSuffix   string
	Provider, AuthState        string
}

// resolveGatewayTarget picks the agent and looks up its gateway port.
//
// ref is an agent id or label; empty means the island's primary. Returns a
// message-bearing error when the agent type has no gateway at all, because
// "nothing happened" is the wrong answer for a CLI- or messaging-only agent.
func resolveGatewayTarget(ctx context.Context, c *api.Client, island, ref string) (gatewayTarget, error) {
	var t gatewayTarget
	isl, err := c.GetIsland(ctx, island)
	if err != nil {
		return t, err
	}
	if len(isl.Agents) == 0 {
		return t, fmt.Errorf("island %q has no agents", island)
	}
	t.Island = island

	if ref != "" {
		id, rerr := project.ResolveAgentRef(isl.Agents, ref)
		if rerr != nil {
			return t, rerr
		}
		t.AgentID = id
		for _, a := range isl.Agents {
			if a.ID == id {
				t.AgentType, t.Provider, t.AuthState = a.Type, a.Provider, a.AuthState
			}
		}
	} else {
		a := isl.Agents[0]
		t.AgentID, t.AgentType, t.Provider, t.AuthState = a.ID, a.Type, a.Provider, a.AuthState
	}

	types, err := c.ListAgentTypes(ctx)
	if err != nil {
		return t, err
	}
	for _, at := range types {
		if at.Type == t.AgentType {
			t.GatewayPort = at.GatewayPort
			t.DashTokenCmd, t.DashSuffix = at.DashboardTokenCmd, at.DashboardTokenSuffix
		}
	}
	if t.GatewayPort == 0 {
		return t, fmt.Errorf("agent type %q has no localhost gateway to open "+
			"(it may be CLI- or messaging-only)", t.AgentType)
	}
	return t, nil
}

// startGatewayForward brings up the ssh forward and waits until the gateway is
// actually serving, then builds the URL to open.
//
// notify fires once if the gateway is not up on the first probe, so a caller can
// explain the wait rather than appearing to hang — a first launch installs the
// framework inside the container and that takes minutes.
//
// It does NOT open a browser and does NOT wait for the process to end. The
// returned forward is live and the caller owns it.
func startGatewayForward(ctx context.Context, t gatewayTarget, localPort int, notify func()) (*gatewayForward, error) {
	host, sshPort, hostKey, enabled, err := resolveSSHEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, fmt.Errorf("%s", sshFacadeSetupSteps())
	}
	// A PREFERRED port is a preference, never a requirement. The caller passes the
	// port this agent used last so a stale browser tab recovers on reload — but
	// something else may hold it now, and with ExitOnForwardFailure=yes ssh would
	// simply die. Falling back to a free port loses the tab-recovery benefit and
	// keeps the feature working, which is the right way round.
	if localPort != 0 && !portFree(localPort) {
		localPort = 0
	}
	if localPort == 0 {
		if localPort, err = freeLocalPort(); err != nil {
			return nil, err
		}
	}
	khArgs, err := managedKnownHostsArgs(host, sshPort, hostKey)
	if err != nil {
		return nil, err
	}

	sshArgs := sshForwardArgs(khArgs, localPort, t.GatewayPort, sshPort, t.Island, host)

	sshCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	sshErr := &lockedBuffer{}
	sshCmd.Stderr = io.MultiWriter(os.Stderr, sshErr)
	if err := sshCmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh forward: %w", err)
	}

	fwd := &gatewayForward{
		Island: t.Island, AgentID: t.AgentID,
		LocalPort: localPort,
		cmd:       sshCmd,
		exited:    make(chan struct{}),
	}
	go func() {
		fwd.waitErr = sshCmd.Wait()
		close(fwd.exited)
	}()

	// Nothing below means anything without a live tunnel, so establish that first
	// and report the real reason rather than probing a container never reached.
	if err := waitForForward(ctx, localPort, fwd.exited, forwardReadyBudget); err != nil {
		fwd.Close()
		if hint := sshFailureHint(sshErr.String(), fwd.waitErr); hint != "" {
			return nil, fmt.Errorf("couldn't open the forward to %s: %s", t.Island, hint)
		}
		return nil, fmt.Errorf("couldn't open the forward to %s: %w", t.Island, err)
	}

	// A BOUND FORWARD IS NOT A RUNNING GATEWAY. ssh binds the local end after
	// auth and does not dial the remote until something connects, so without this
	// the browser opens on a port with nothing behind it — which is the entire
	// first-launch window while the framework installs itself.
	if !waitForGateway(ctx, localPort, fwd.exited, gatewayReadyBudget, notify) {
		fwd.Close()
		return nil, fmt.Errorf("the tunnel to %s came up but nothing is serving the gateway yet", t.Island)
	}

	fwd.URL = fmt.Sprintf("http://localhost:%d/", localPort)
	if t.DashTokenCmd != "" {
		raw, _ := probeGatewayToken(ctx, khArgs, sshPort, t.Island, host, t.DashTokenCmd)
		if token := parseTokenOutput(raw); token != "" {
			suffix := t.DashSuffix
			if suffix == "" {
				suffix = "#token={token}"
			}
			fwd.URL += strings.ReplaceAll(suffix, "{token}", neturl.QueryEscape(token))
		}
	}
	return fwd, nil
}
