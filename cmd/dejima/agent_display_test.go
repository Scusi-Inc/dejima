package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestAgentDisplay covers the shared name-first formatter: "label (id)" when the
// agent has a label (the #190 convention), the bare id as a fallback when the
// label is blank, and the bare label when there's no id to disambiguate.
func TestAgentDisplay(t *testing.T) {
	cases := []struct {
		name      string
		label, id string
		want      string
	}{
		{"label and id", "backend", "a1", "backend (a1)"},
		{"blank label falls back to id", "", "a1", "a1"},
		{"whitespace label falls back to id", "   ", "a2", "a2"},
		{"label with no id", "backend", "", "backend"},
		{"both blank", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentDisplay(tc.label, tc.id); got != tc.want {
				t.Errorf("agentDisplay(%q, %q) = %q, want %q", tc.label, tc.id, got, tc.want)
			}
		})
	}
}

// TestCLIAgentLsNameFirst asserts `dejima agent ls` leads with the agent NAME
// column (its label) and still carries the id for disambiguation — the rename
// the operator gives an agent shows up first in the listing.
func TestCLIAgentLsNameFirst(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	// Add a second, explicitly-labelled agent so the listing shows a name that
	// isn't just the type-derived default.
	if _, err := c.AddAgent(context.Background(), "proj", api.AgentSpecRequest{
		Type: "claude-code", Label: "backend",
	}); err != nil {
		t.Fatalf("add labelled agent: %v", err)
	}

	out, err := runCLI(t, "agent", "ls", "proj")
	if err != nil {
		t.Fatalf("agent ls: %v", err)
	}
	// Name-first header.
	if !strings.Contains(out, "NAME") {
		t.Errorf("agent ls header should lead with NAME: %q", out)
	}
	// The given label appears (name surface).
	if !strings.Contains(out, "backend") {
		t.Errorf("agent ls should show the agent's label %q: %q", "backend", out)
	}
	// The NAME column must precede the ID column in the header row.
	header := out
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		header = out[:i]
	}
	if ni, idi := strings.Index(header, "NAME"), strings.Index(header, "ID"); ni < 0 || idi < 0 || ni > idi {
		t.Errorf("NAME should come before ID in the header: %q", header)
	}
}
