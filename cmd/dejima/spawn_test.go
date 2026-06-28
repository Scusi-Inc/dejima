package main

import (
	"context"
	"io"
	"testing"
)

// TestSpawnCmdTree checks the `dejima spawn` subcommands are wired.
func TestSpawnCmdTree(t *testing.T) {
	cmd := newSpawnCmd()
	want := map[string]bool{"grant <island>": false, "show <island>": false, "revoke <island>": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Use]; ok {
			want[sub.Use] = true
		}
	}
	for use, found := range want {
		if !found {
			t.Errorf("spawn subcommand %q not registered", use)
		}
	}
}

// TestSpawnCmdArgValidation exercises each subcommand's arg contract through the
// cobra tree (validation fires before any daemon call). Also the coverage gate's
// reference for the `spawn *` commands.
func TestSpawnCmdArgValidation(t *testing.T) {
	cases := [][]string{
		{"spawn", "grant"},  // needs <island>
		{"spawn", "show"},   // needs <island>
		{"spawn", "revoke"}, // needs <island>
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
