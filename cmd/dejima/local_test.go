package main

import "testing"

// TestLocalCommandTree asserts the `dejima local` family is reachable from the
// binary, verb by verb. It walked newLocalCmd() directly until the paths moved
// into code: that form proves the subcommands hang off a constructor, not that
// anything attached the constructor to the root.
func TestLocalCommandTree(t *testing.T) {
	if use := newLocalCmd().Use; use != "local" {
		t.Fatalf("family command Use = %q, want %q", use, "local")
	}
	requirePaths(t, rootCommandPaths(t),
		"dejima local status",
		"dejima local install",
		"dejima local models",
		"dejima local pull",
		"dejima local rm",
		"dejima local off",
	)
}
