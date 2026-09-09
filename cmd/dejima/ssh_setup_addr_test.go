package main

import (
	"strings"
	"testing"
)

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

// The façade guidance must be a PANE, and must survive being read.
//
// It was routed through m.lastError, which renderFooterLeft truncates at 60
// characters. The condensed steps are ~180, so pressing ⏎ on an OpenClaw agent
// produced a red bar cut off mid-command:
//
//	⚠ gateway UI needs the SSH façade. 1) on the daemon host: su
//
// A named prerequisite and no way to act on it.
func TestFacadeGuidanceSurvivesTheFooterTruncation(t *testing.T) {
	if got := len(sshFacadeSetupStepsTUI()); got <= 60 {
		t.Skipf("the one-line form is now %d chars and would survive the footer; "+
			"this test's premise no longer holds", got)
	}
	pane := sshFacadeHelpPane("100.89.51.27:7273", true,
		"sudo dejima service install --system --ssh 100.89.51.27:2222", false)

	// The whole command has to be present, not a prefix of it.
	if !strings.Contains(pane, "--ssh 100.89.51.27:2222") {
		t.Errorf("the enable command is incomplete in the pane:\n%s", pane)
	}
	if len(pane) <= 60 {
		t.Errorf("the pane is footer-sized, so it is still the truncated form:\n%s", pane)
	}
}

// The two steps are on two different machines, and a remote daemon has to say
// so — a laptop driving a Mac mini is not the machine step 1 runs on.
func TestFacadeGuidanceNamesWhichMachine(t *testing.T) {
	remote := sshFacadeHelpPane("100.89.51.27:7273", true, "sudo dejima service install --ssh x:2222", false)
	if !strings.Contains(remote, "DAEMON HOST") {
		t.Errorf("step 1 does not name the machine it runs on:\n%s", remote)
	}
	if !strings.Contains(remote, "not this one") {
		t.Errorf("nothing rules out the machine reading it, which is the whole "+
			"wrong-machine failure:\n%s", remote)
	}
	if !strings.Contains(remote, "100.89.51.27:7273") {
		t.Errorf("the daemon host is not identified:\n%s", remote)
	}

	// On the host itself, "not this one" would be a lie.
	local := sshFacadeHelpPane("local", false, "sudo dejima service install --ssh x:2222", true)
	if strings.Contains(local, "not this one") {
		t.Errorf("a local daemon was described as a different machine:\n%s", local)
	}
}

// Step 2 points at a menu entry that DOES NOT EXIST until step 1 is done — the
// action is gated on overview.SSHAddr. An operator who goes looking for it early
// finds nothing and concludes the instructions are wrong, so the pane has to say
// it is a sequence.
func TestFacadeGuidanceExplainsTheMissingMenuEntry(t *testing.T) {
	pane := sshFacadeHelpPane("100.89.51.27:7273", true, "sudo dejima service install --ssh x:2222", false)
	if !strings.Contains(pane, "SSH setup") {
		t.Errorf("step 2 does not name the action to take:\n%s", pane)
	}
	if !strings.Contains(pane, "after step 1") {
		t.Errorf("the pane does not say the entry appears only after step 1, so a "+
			"reader who checks the menu now finds a dead end:\n%s", pane)
	}
	if !strings.Contains(pane, "esc") {
		t.Errorf("no way out of the pane is named:\n%s", pane)
	}
}
