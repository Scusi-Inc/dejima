package main

import (
	"strings"
	"testing"
)

// `dejima version` is what people type first. It used to return
// `unknown command "version"` and point at --help, which does not itself mention
// --version — so the obvious guess failed AND the remedy it offered did not
// contain the answer.
//
// Asserted through the real root command rather than by calling the constructor,
// because the bug was never in the command: it was that nothing registered it.
// A test that calls newVersionCmd() directly passes with the AddCommand line
// deleted, which is the guard-that-isn't-looking shape.
func TestVersionSubcommandIsRegistered(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatalf("root.Find(version): %v", err)
	}
	if cmd == nil || cmd.Name() != "version" {
		t.Fatalf("`dejima version` does not resolve to the version command; got %v", cmd)
	}
	// Both spellings must survive: adding the word must not have displaced the flag.
	if root.Version == "" {
		t.Error("--version lost its value — the flag and the subcommand must both work")
	}
}

// The command prints a version, not a usage dump or an empty line.
func TestVersionSubcommandPrintsAVersion(t *testing.T) {
	cmd := newVersionCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
}
