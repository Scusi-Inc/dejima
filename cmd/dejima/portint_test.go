package main

import (
	"strings"
	"testing"
)

// A saved profile read :7373 rather than :7273 — a transposition, invisible in a
// line the operator had read a dozen times, and indistinguishable from a down
// server in every message we showed them. Their other machine worked, which made
// it look like the SERVER was refusing this one.
func TestNonDefaultPortIsPointedOut(t *testing.T) {
	got := nonDefaultPortHint("100.101.102.103:7373")
	if got == "" {
		t.Fatal("a transposed port produced no hint; this is the failure that cost an evening")
	}
	for _, want := range []string{"7373", defaultDaemonTCPPort} {
		if !strings.Contains(got, want) {
			t.Errorf("hint does not put %q in front of the reader: %q", want, got)
		}
	}
	// It must not ASSERT the port is wrong — a deliberate non-default port is
	// legitimate, and a confident wrong diagnosis is what this codebase has
	// spent the week removing.
	lower := strings.ToLower(got)
	for _, forbidden := range []string{"is wrong", "incorrect", "invalid"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("hint asserts the port is wrong rather than showing both numbers: %q", got)
		}
	}
}

// The default must be silent. Firing on every unreachable host would make the
// hint noise, and noise is how a real signal gets skipped.
func TestDefaultPortProducesNoHint(t *testing.T) {
	for _, host := range []string{
		"100.101.102.103:" + defaultDaemonTCPPort,
		"mac-mini:" + defaultDaemonTCPPort,
		"http://mac-mini:" + defaultDaemonTCPPort,
	} {
		if h := nonDefaultPortHint(host); h != "" {
			t.Errorf("hinted on a default-port host %q: %q", host, h)
		}
	}
}

// Hosts with no port at all, or shapes we cannot parse, must produce nothing
// rather than a guess.
func TestUnparseableHostsProduceNoHint(t *testing.T) {
	for _, host := range []string{"", "  ", "mac-mini", "wsl://dejima", "not a host"} {
		if h := nonDefaultPortHint(host); h != "" {
			t.Errorf("invented a hint for %q: %q", host, h)
		}
	}
}

// End to end: the hint reaches the steps a person actually reads.
func TestHintAppearsInTheRemoteDiagnosis(t *testing.T) {
	d := diagnoseRemoteDaemon("100.101.102.103:7373")
	joined := strings.Join(d.Steps, "\n")
	if !strings.Contains(joined, "7373") {
		t.Errorf("the port hint never reaches the displayed steps:\n%s", joined)
	}
	// And the default case must not gain an empty bullet.
	d = diagnoseRemoteDaemon("100.101.102.103:" + defaultDaemonTCPPort)
	for _, s := range d.Steps {
		if strings.TrimSpace(s) == "" {
			t.Error("an empty step survived into the displayed list")
		}
	}
}
