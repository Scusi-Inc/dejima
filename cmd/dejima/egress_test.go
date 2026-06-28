package main

import (
	"context"
	"io"
	"testing"
)

// TestEgressCmdTree checks the `dejima egress` subcommands are all wired.
func TestEgressCmdTree(t *testing.T) {
	cmd := newEgressCmd()
	want := map[string]bool{
		"show <island>": false, "policy <island>": false,
		"mode <island> <observe|enforce>": false,
		"allow <island> <host>...":        false,
		"deny <island> <host>...":         false,
		"rm <island> <host>...":           false,
	}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Use]; ok {
			want[sub.Use] = true
		}
	}
	for use, found := range want {
		if !found {
			t.Errorf("egress subcommand %q not registered", use)
		}
	}
}

// TestEgressCmdArgValidation exercises each subcommand's arg contract through the
// cobra tree (validation fires before any daemon call, so no daemon is needed).
// It is also the coverage gate's reference for the `egress *` commands.
func TestEgressCmdArgValidation(t *testing.T) {
	cases := [][]string{
		{"egress", "show"},         // needs <island>
		{"egress", "policy"},       // needs <island>
		{"egress", "mode", "isl"},  // needs <island> <mode>
		{"egress", "allow", "isl"}, // needs <island> <host>...
		{"egress", "deny", "isl"},  // needs <island> <host>...
		{"egress", "rm", "isl"},    // needs <island> <host>...
	}
	for _, args := range cases {
		root := newRootCmd()
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.ExecuteContext(context.Background()); err == nil {
			t.Errorf("%v: expected an arg-validation error, got nil", args)
		}
	}
}
