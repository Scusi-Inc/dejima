package main

import (
	"strings"
	"testing"
)

// TestLocalCommandTree asserts the `dejima local` family is wired with all its
// subcommands. The command paths it covers (also the coverage-gate keys):
//
//	dejima local status
//	dejima local install
//	dejima local models
//	dejima local pull
//	dejima local rm
//	dejima local off
func TestLocalCommandTree(t *testing.T) {
	cmd := newLocalCmd()
	if cmd.Use != "local" {
		t.Fatalf("root command Use = %q, want %q", cmd.Use, "local")
	}
	got := map[string]bool{}
	for _, sub := range cmd.Commands() {
		got[strings.Fields(sub.Use)[0]] = true
	}
	for _, name := range []string{"status", "install", "models", "pull", "rm", "off"} {
		if !got[name] {
			t.Errorf("`dejima local %s` subcommand is not registered", name)
		}
	}
}
