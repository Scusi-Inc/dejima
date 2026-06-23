package main

import (
	"context"
	"testing"
)

// sshProbeHost is the pure host-normalization behind probeSSH — strip a
// user-appended :port, leave bare names/IPs (incl. bare IPv6) intact. Tested
// directly because probeSSH's live dial is environment-dependent.
func TestSSHProbeHost(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"minion.ts.net":      "minion.ts.net",
		"minion.ts.net:7273": "minion.ts.net",
		"10.0.0.5":           "10.0.0.5",
		"10.0.0.5:22":        "10.0.0.5",
		"[::1]:7273":         "::1",
		"::1":                "::1",
		"  host  ":           "host", // trimmed
	}
	for in, want := range cases {
		if got := sshProbeHost(in); got != want {
			t.Errorf("sshProbeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// An empty host short-circuits to false without dialing (deterministic).
func TestProbeSSHEmpty(t *testing.T) {
	if probeSSH(context.Background(), "") {
		t.Error("probeSSH(\"\") should be false")
	}
}
