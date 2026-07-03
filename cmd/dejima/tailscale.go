package main

import (
	"fmt"
	"net"
	"strings"
)

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
