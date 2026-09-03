package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// rootCommandPaths returns every runnable command in the SHIPPED tree, keyed by
// the path an operator types: "dejima local models".
//
// Family tests used to walk their own constructor — `newLocalCmd().Commands()`
// — which proves the subcommands exist on a command nobody may have attached to
// anything. Walking from newRootCmd() asserts the stronger and more useful
// thing: the verb is reachable from the binary. A family that loses its
// AddCommand call passes the constructor form and fails this one.
//
// It also puts the full path in CODE. The coverage gate credits a command when
// the corpus references it, and these families were being credited by the
// sentence above their test rather than by the test — which the gate then read
// as coverage. See corpusText in coverage_gate_test.go.
func rootCommandPaths(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() {
			out[c.CommandPath()] = true
		}
		for _, ch := range c.Commands() {
			walk(ch)
		}
	}
	walk(newRootCmd())
	// The control: a walker that returns an empty (or tiny) set would make every
	// caller below pass by finding nothing to disagree with.
	if len(out) < 50 {
		t.Fatalf("walked only %d runnable commands from the root — the walker is broken, "+
			"and every command-tree assertion in this package is vacuous until it is fixed", len(out))
	}
	return out
}

// requirePaths asserts each path is reachable from the root binary.
func requirePaths(t *testing.T, paths map[string]bool, want ...string) {
	t.Helper()
	for _, w := range want {
		if !paths[w] {
			t.Errorf("`%s` is not wired into the root command", w)
		}
	}
}
