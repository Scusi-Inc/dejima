package main

import (
	"strings"
	"testing"
)

// The daemon's connection errors are written for whoever debugs the daemon.
// They surface verbatim to someone who is uninstalling and, at that moment,
// wants to know one thing: is my work safe? "invalid token" answers that for
// nobody — it was reported from a real uninstall as the opening line, above a
// list headed "This will permanently:".
func TestPlainDaemonErrorSaysWhatItMeans(t *testing.T) {
	cases := map[string]string{
		"invalid token":    "saved sign-in",
		"401 unauthorized": "saved sign-in",
		"dial unix /…/dejimad.sock: connection refused": "isn't running",
		"context deadline exceeded":                     "can't reach it",
	}
	for raw, want := range cases {
		if got := plainDaemonError(raw); !strings.Contains(got, want) {
			t.Errorf("plainDaemonError(%q) = %q, want it to mention %q", raw, got, want)
		}
	}
}

// An unrecognised error must pass through rather than be flattened into a vague
// reassurance — an operator can search the real string; they cannot search
// "something went wrong".
func TestPlainDaemonErrorPassesThroughTheUnknown(t *testing.T) {
	const raw = "tls: handshake failure on port 7273"
	if got := plainDaemonError(raw); got != raw {
		t.Errorf("an unrecognised error was rewritten: %q → %q", raw, got)
	}
}

// The closing notice is the last thing read, and for a non-engineer it has to
// answer "did that work, and where did my stuff go" without assuming they know
// what a daemon, a volume, or a host is.
func TestDaemonDownNoticeLeadsWithTheOutcome(t *testing.T) {
	got := daemonDownNotice(false)

	if !strings.HasPrefix(strings.TrimSpace(got), "Done —") {
		t.Errorf("the notice must open by saying it worked:\n%s", got)
	}
	if !strings.Contains(got, "Nothing of yours was deleted") {
		t.Errorf("the reassurance a person is actually looking for is missing:\n%s", got)
	}
	// Islands may well live on a different Mac than the one being uninstalled —
	// that is the case this notice exists for, and the old text asserted the
	// leftovers were here.
	if !strings.Contains(got, "may not be this one") {
		t.Errorf("the notice assumes the islands are on this Mac:\n%s", got)
	}
}
