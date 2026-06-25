package main

import (
	"strings"
	"testing"
)

// TestCLIPolicy drives `dejima policy add/ls/rm` against an in-proc daemon.
func TestCLIPolicy(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "alpha")
	seedIsland(t, c, "beta")

	// Empty.
	out, err := runCLI(t, "policy", "ls")
	if err != nil {
		t.Fatalf("policy ls (empty): %v", err)
	}
	if !strings.Contains(out, "no auto-approve rules") {
		t.Errorf("empty ls should explain prompt-everything; got %q", out)
	}

	// Add.
	out, err = runCLI(t, "policy", "add", "--link", "alpha->beta", "--action", "dispatch", "--max", "5", "--ttl", "1h")
	if err != nil {
		t.Fatalf("policy add: %v", err)
	}
	if !strings.Contains(out, "rule added") || !strings.Contains(out, "alpha→beta") {
		t.Errorf("add output unexpected: %q", out)
	}

	// List shows it.
	out, err = runCLI(t, "policy", "ls")
	if err != nil {
		t.Fatalf("policy ls: %v", err)
	}
	if !strings.Contains(out, "alpha→beta") || !strings.Contains(out, "dispatch") {
		t.Errorf("ls should show the rule; got %q", out)
	}

	// Remove.
	out, err = runCLI(t, "policy", "rm", "--link", "alpha->beta", "--action", "dispatch")
	if err != nil {
		t.Fatalf("policy rm: %v", err)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("rm output unexpected: %q", out)
	}

	// Removing again fails (gone).
	if _, err := runCLI(t, "policy", "rm", "--link", "alpha->beta", "--action", "dispatch"); err == nil {
		t.Error("removing a nonexistent rule should error")
	}

	// Bad --link is rejected before any call.
	if _, err := runCLI(t, "policy", "add", "--link", "alpha", "--action", "x"); err == nil {
		t.Error("malformed --link should error")
	}
}
