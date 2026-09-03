package main

import (
	"regexp"
	"strings"
	"testing"
)

// The `dejima agent open` tunnel exists to be LEFT OPEN — a console is something
// you open, walk away from, and come back to. That traffic profile is long idle
// gaps, which is exactly what NAT tables and stateful firewalls reap.
//
// Without ServerAliveInterval, ssh sends nothing during those gaps. The path is
// dropped silently, and the browser tab reports the GATEWAY as disconnected —
// so a dead tunnel presents as a broken agent. That is the reported symptom on
// the operator's machine: it opened, it was logged in, and LATER it said
// "Disconnected from gateway".
//
// Source-level assertions, because the alternative is a live tunnel over a real
// NAT with an idle timeout, which no unit test can stage. Both are anchored to
// the argument slice actually handed to exec, not to a comment about it.
func TestAgentOpenSetsSSHKeepalive(t *testing.T) {
	src := readSource(t, "gateway_forward.go")

	// Scope to the literal that builds ssh's argv. Searching the whole file
	// would match these strings in the explanatory comment above them and pass
	// while the flags were absent from the command.
	args := sshArgsLiteral(t, src)

	for _, want := range []string{"ServerAliveInterval", "ServerAliveCountMax"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh argv is missing %s — an idle tunnel will be dropped with no "+
				"keepalive, and the console will blame the gateway.\nargv literal:\n%s", want, args)
		}
	}
	// ExitOnForwardFailure is what makes a successful local bind mean ssh
	// authenticated and accepted the forward. waitForForward's correctness
	// depends on it, so it must survive any edit to this list.
	if !strings.Contains(args, "ExitOnForwardFailure") {
		t.Errorf("ExitOnForwardFailure dropped from ssh argv — waitForForward's "+
			"readiness check silently stops meaning anything.\nargv literal:\n%s", args)
	}
}

// When ssh ends after the forward WAS established, the browser tab stays open
// pointing at a local port nothing is listening on. Every console renders that
// as its own disconnection, so silence here means the user reads a gateway
// error for a gateway that never failed.
//
// The common case prints nothing without this: a dropped path exits ssh 0, so
// waitErr is nil and the command returns quietly.
func TestAgentOpenReportsAClosedForward(t *testing.T) {
	src := readSource(t, "agent_open.go")
	if !strings.Contains(src, "has closed — the console in your browser is now pointing at nothing") {
		t.Error("nothing tells the user the tunnel closed; a dead console is " +
			"indistinguishable from a dead gateway")
	}
	// Suppressed on deliberate teardown — confirming a Ctrl-C is noise.
	if !strings.Contains(src, "if cmd.Context().Err() == nil {") {
		t.Error("the closed-forward notice isn't gated on deliberate teardown; " +
			"it will fire on every Ctrl-C")
	}
}

// The argv builder MOVED to gateway_forward.go when the dashboard began holding
// its own forward, so both callers construct it identically. This guard followed
// it rather than being deleted — and it FAILED rather than passing when the
// literal vanished, which is the only reason the move was noticed at all.

// sshArgsLiteral extracts the `sshArgs := []string{…}` composite literal so
// assertions can't be satisfied by prose elsewhere in the file.
var sshArgsRe = regexp.MustCompile(`(?s)args := \[\]string\{(.*?)\n\t*\}`)

func sshArgsLiteral(t *testing.T, src string) string {
	t.Helper()
	m := sshArgsRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("couldn't find the argv literal in sshForwardArgs — this test " +
			"asserts against ssh's real argv, and silently matching nothing " +
			"would make it pass for the wrong reason")
	}
	return m[1]
}
