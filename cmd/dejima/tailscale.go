package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// defaultDaemonTCPPort is the port the daemon's Tailscale-pinned TCP listener
// uses by default (`dejima service install --tcp :7273`). Used to build a
// reachable invite host from a bare tailnet IP.
const defaultDaemonTCPPort = "7273"

// tailscaleIPLookup is indirected so tests can stub the tailnet-IP capture
// without a real `tailscale` binary.
var tailscaleIPLookup = tailscaleIPv4

// tailscaleFQDNLookup is indirected (like tailscaleIPLookup) so tests can stub
// the MagicDNS-name capture without a real `tailscale` binary.
var tailscaleFQDNLookup = sshTailnetFQDN

// daemonInviteHost returns the address a teammate should dial to reach the LOCAL
// daemon over the tailnet, preferring the host's MagicDNS name
// (<host>.<tailnet>.ts.net) over the raw tailnet IPv4.
//
// Why prefer the name: a MagicDNS name resolves to the right address from every
// tailnet — INCLUDING one the node is *shared* into, where Tailscale often
// re-addresses the node so the raw 100.x IP differs from what the operator sees.
// Baking a raw IP into an invite therefore strands a node-shared teammate on an
// address that doesn't exist in their tailnet; the name doesn't have that
// failure mode. rawIP reports that only an IP was available (MagicDNS off), so
// callers can warn.
func daemonInviteHost() (hostPort string, rawIP bool, ok bool) {
	if fqdn := strings.TrimSpace(tailscaleFQDNLookup()); fqdn != "" {
		return net.JoinHostPort(fqdn, defaultDaemonTCPPort), false, true
	}
	if ip, ok := tailscaleIPLookup(); ok {
		return net.JoinHostPort(ip, defaultDaemonTCPPort), true, true
	}
	return "", false, false
}

// isRawTailscaleIP reports whether hostPort is a Tailscale host expressed as a
// raw 100.x/fd7a IP rather than a MagicDNS "*.ts.net" name — the fragile form
// for a teammate reached via node-sharing, since that IP can be remapped in the
// teammate's tailnet. Used to warn the operator at invite time.
func isRawTailscaleIP(hostPort string) bool {
	if !isTailscaleHost(hostPort) {
		return false
	}
	return net.ParseIP(hostOnly(hostPort)) != nil
}

// tailscaleIPv4 returns this machine's Tailscale IPv4 address via `tailscale ip
// -4`, if Tailscale is installed and up. It's used to prefill the invite host
// when the operator mints FROM the host itself (local socket): the daemon can't
// self-detect its reachable address, but the host's own tailnet IP is exactly
// the address a teammate dials. Bounded so a hung binary can't stall the UI.
func tailscaleIPv4() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return "", false
	}
	return parseTailscaleIPv4(string(out))
}

// parseTailscaleIPv4 picks the first Tailscale IPv4 address out of `tailscale ip`
// output (which may list several addresses, one per line). Split out from the
// exec so the parsing is unit-testable.
func parseTailscaleIPv4(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		ip := strings.TrimSpace(line)
		if ip == "" {
			continue
		}
		if p := net.ParseIP(ip); p != nil && p.To4() != nil && isTailscaleHost(ip) {
			return ip, true
		}
	}
	return "", false
}

// hostOnly strips the port (and any IPv6 brackets) from a host[:port], returning
// just the address/name for classification.
func hostOnly(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return strings.Trim(hostPort, "[]")
}

// isTailscaleHost reports whether hostPort points at a Tailscale address: the
// 100.64.0.0/10 CGNAT range (IPv4) or the fd7a:115c:a1e0::/48 ULA prefix (IPv6)
// Tailscale assigns, or a MagicDNS "*.ts.net" name. A Tailscale-pinned daemon is
// reachable only from peers on the same tailnet, so a joining teammate who isn't
// on it needs guiding — this gates that. A plain (non-Tailscale) DNS name is not
// classifiable by address, so we don't over-warn on it.
func isTailscaleHost(hostPort string) bool {
	h := hostOnly(hostPort)
	if h == "" {
		return false
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(h, ".")), ".ts.net") {
		return true // MagicDNS name
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 100 && v4[1]&0xC0 == 0x40 // 100.64.0.0/10
	}
	// fd7a:115c:a1e0::/48
	return len(ip) == net.IPv6len && ip[0] == 0xfd && ip[1] == 0x7a &&
		ip[2] == 0x11 && ip[3] == 0x5c && ip[4] == 0xa1 && ip[5] == 0xe0
}

// tcpReachable reports whether hostPort accepts a TCP connection within a short
// timeout — a direct check that a daemon's tailnet listener is actually up, used
// to VERIFY (not just claim) remote reachability at the end of provisioning.
func tcpReachable(hostPort string) bool {
	c, err := net.DialTimeout("tcp", hostPort, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// printTailscaleJoinHelp replaces the opaque "context deadline exceeded" a
// teammate would otherwise hit when they join a Tailscale-pinned daemon without
// being on its tailnet. The profile is already saved, so a retry after joining
// the tailnet Just Works.
func printTailscaleJoinHelp(hostPort string) {
	host := hostOnly(hostPort)
	fmt.Println()
	fmt.Println(bold("This server is on Tailscale (" + host + ") — your computer can't reach its tailnet yet."))
	fmt.Println("Your profile is saved, so once you're on the tailnet just re-run the same command.")
	fmt.Println("Being *signed in* to Tailscale isn't enough — you have to be on THIS server's tailnet:")
	fmt.Println("    1. The Dejima installer already set Tailscale up for you. If you skipped it:")
	fmt.Println("       curl -fsSL https://tailscale.com/install.sh | sh   (or `brew install tailscale`)")
	fmt.Println("    2. Ask the operator to share this node with you (Tailscale admin console →")
	fmt.Println("       Machines → Share). Open the share link they send and ACCEPT it — that's the")
	fmt.Println("       step that puts the server on your tailnet.")
	fmt.Printf("    3. Confirm you can reach it:  tailscale ping %s\n", host)
	fmt.Println("    4. Re-run `dejima join <invite>` (or just `dejima`) — the saved profile connects.")
	if isRawTailscaleIP(hostPort) {
		fmt.Println()
		fmt.Println("  Note: this invite uses a raw tailnet IP. If the server was *shared* with you,")
		fmt.Println("  your Tailscale may list it under a DIFFERENT 100.x address than the one above —")
		fmt.Printf("  check `tailscale status` for the shared node's IP. Ask the operator to re-issue\n")
		fmt.Println("  the invite with the server's MagicDNS name (*.ts.net), which avoids this entirely.")
	}
	fmt.Println()
}
