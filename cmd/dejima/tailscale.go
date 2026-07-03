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

// printTailscaleJoinHelp replaces the opaque "context deadline exceeded" a
// teammate would otherwise hit when they join a Tailscale-pinned daemon without
// being on its tailnet. The profile is already saved, so a retry after joining
// the tailnet Just Works.
func printTailscaleJoinHelp(hostPort string) {
	fmt.Println()
	fmt.Println(bold("This server is on Tailscale (" + hostOnly(hostPort) + ") — your computer isn't on its tailnet yet."))
	fmt.Println("Your profile is saved. To connect, get on the tailnet, then re-run the same command:")
	fmt.Println("    1. Install Tailscale:  https://tailscale.com/download")
	fmt.Println("    2. Join the tailnet — ask your teammate to share this node")
	fmt.Println("       (Tailscale admin console → Machines → Share), accept it, then `tailscale up`.")
	fmt.Println("    3. Re-run `dejima join <invite>` (or just `dejima`) — the saved profile will connect.")
	fmt.Println()
}
