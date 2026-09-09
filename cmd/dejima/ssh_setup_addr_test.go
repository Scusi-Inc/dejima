package main

import "testing"

// The façade address belongs to the DAEMON HOST, which is routinely not the
// machine typing.
//
// suggestedSSHAddr asked the local `tailscale ip -4`. That is this device's
// address, and it lands in a command the operator is told to run with sudo ON
// THE HOST, as the address the façade BINDS. A Mac driving a Mac mini was told
// to bind the mini's façade to the laptop's tailnet IP — a command for one
// machine composed from facts about another, which is the same shape as the
// `ollama serve` advice on the wrong computer.
//
// When the daemon is remote we already know its address: it is the host we are
// connected to.
func TestSSHAddrComesFromTheDaemonHost(t *testing.T) {
	// The reported setup: a remote daemon reached over the tailnet on the API
	// port. The façade port replaces it — 7273 is the API listener, 2222 is not.
	if got := sshAddrFromDaemonHost("100.89.51.27:7273"); got != "100.89.51.27:2222" {
		t.Errorf("sshAddrFromDaemonHost = %q, want the DAEMON's address on 2222", got)
	}
	// A hostname works the same way.
	if got := sshAddrFromDaemonHost("mac-mini.tailnet.ts.net:7273"); got != "mac-mini.tailnet.ts.net:2222" {
		t.Errorf("hostname target = %q", got)
	}
	// IPv6 has to survive the round-trip bracketed, or the command is unusable.
	if got := sshAddrFromDaemonHost("[fd7a::1]:7273"); got != "[fd7a::1]:2222" {
		t.Errorf("ipv6 target = %q, want a bracketed host", got)
	}
}

// Targets with no usable address must fall through, NOT produce a bogus one —
// the local socket and wsl:// are reached by other means entirely, and a
// half-parsed string in a sudo command is worse than the placeholder.
func TestSSHAddrFallsThroughWhenThereIsNoHost(t *testing.T) {
	for _, host := range []string{"", "   ", "wsl://Ubuntu", "no-port-here"} {
		if got := sshAddrFromDaemonHost(host); got != "" {
			t.Errorf("sshAddrFromDaemonHost(%q) = %q, want \"\" so the caller falls back", host, got)
		}
	}
}
