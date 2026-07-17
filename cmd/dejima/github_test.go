package main

import "testing"

// TestNewGithubConnectCmdShape exercises the `dejima github connect` command:
// name, the [name] arg is optional and at most one, and the owner-only + no-open
// flags are present.
func TestNewGithubConnectCmdShape(t *testing.T) {
	root := newGithubCmd()
	if root.Name() != "github" {
		t.Fatalf("group name = %q, want github", root.Name())
	}
	sub, _, err := root.Find([]string{"connect"})
	if err != nil || sub.Name() != "connect" {
		t.Fatalf("`github connect` subcommand missing: sub=%v err=%v", sub, err)
	}
	for _, f := range []string{"default", "shared", "no-open"} {
		if sub.Flags().Lookup(f) == nil {
			t.Errorf("connect should expose --%s", f)
		}
	}
	if err := sub.Args(sub, []string{}); err != nil {
		t.Errorf("connect with no name arg should be valid: %v", err)
	}
	if err := sub.Args(sub, []string{"one"}); err != nil {
		t.Errorf("connect with one name arg should be valid: %v", err)
	}
	if err := sub.Args(sub, []string{"one", "two"}); err == nil {
		t.Error("connect takes at most one name arg")
	}
}
