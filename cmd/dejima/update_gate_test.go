package main

import (
	"context"
	"strings"
	"testing"
)

// TestAttachedClientCountKnown: against a reachable daemon (the in-proc httptest
// server wired via DEJIMA_HOST by cliEnv), attachedClientCount reports a known
// count — zero on a fresh daemon with nothing attached. This is the signal the
// `dejima update` source path uses to refuse a daemon-restarting update.
func TestAttachedClientCountKnown(t *testing.T) {
	cliEnv(t)
	n, known := attachedClientCount(context.Background())
	if !known {
		t.Fatal("a reachable daemon should yield a known attached-client count")
	}
	if n != 0 {
		t.Errorf("a fresh daemon should report 0 attached clients, got %d", n)
	}
}

// TestAttachedClientCountUnreachable: with DEJIMA_HOST pointed at a dead address
// the count is unknown (not silently zero) — so the CLI warns rather than
// hard-blocking a source update when it genuinely cannot verify.
func TestAttachedClientCountUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_TOKEN", "")
	t.Setenv("DEJIMA_HOST", "127.0.0.1:0") // nothing listening
	if _, known := attachedClientCount(context.Background()); known {
		t.Error("an unreachable daemon should yield known=false, not a silent 0")
	}
}

// TestPluralClients: the refusal message pluralizes the attached-terminal count.
func TestPluralClients(t *testing.T) {
	if got := pluralClients(1); !strings.Contains(got, "1 terminal is") {
		t.Errorf("pluralClients(1) = %q", got)
	}
	if got := pluralClients(3); !strings.Contains(got, "3 terminals are") {
		t.Errorf("pluralClients(3) = %q", got)
	}
}
