package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestVoiceCommandTree pins the command surface: `dejima voice`, with the
// `dejima voice install` and `dejima voice status` subcommands. It also runs
// `voice status` (read-only — probes the local toolchain, no network, no
// mutation) so the status path has real coverage. `voice` (needs an island +
// mic) and `voice install` (would shell out to Homebrew) are structural-only
// here.
func TestVoiceCommandTree(t *testing.T) {
	root := newVoiceCmd()
	if !strings.HasPrefix(root.Use, "voice") {
		t.Fatalf("root.Use = %q, want it to start with voice", root.Use)
	}
	subs := map[string]bool{}
	for _, c := range root.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"install", "status"} {
		if !subs[want] {
			t.Errorf("dejima voice is missing the %q subcommand", want)
		}
	}

	// Exercise `dejima voice status` end-to-end. On a host with no toolchain it
	// reports "not set up"; on a set-up host, "ready". Either is a clean run.
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("dejima voice status errored: %v", err)
	}
}

func TestJoinAnd(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b and c"},
		{[]string{"whisper.cpp CLI", "a recorder (sox)", "the model"}, "whisper.cpp CLI, a recorder (sox) and the model"},
	}
	for _, c := range cases {
		if got := joinAnd(c.in); got != c.want {
			t.Errorf("joinAnd(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
