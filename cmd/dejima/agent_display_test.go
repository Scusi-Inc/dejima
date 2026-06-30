package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestAgentDisplay covers the shared name-first formatter. Default is
// LABEL-ONLY — agents are referred to by name; the bare id is an internal handle
// shown only when --ids/DEJIMA_SHOW_IDS reveals it. A nameless agent always falls
// back to the id so nothing renders blank.
func TestAgentDisplay(t *testing.T) {
	defer func(v bool) { showIDs = v }(showIDs)
	cases := []struct {
		name      string
		ids       bool
		label, id string
		want      string
	}{
		// Default: names only.
		{"label only by default", false, "backend", "a1", "backend"},
		{"blank label falls back to id", false, "", "a1", "a1"},
		{"whitespace label falls back to id", false, "   ", "a2", "a2"},
		{"label with no id", false, "backend", "", "backend"},
		{"both blank", false, "", "", ""},
		// --ids reveals the id alongside the name.
		{"label and id revealed", true, "backend", "a1", "backend (a1)"},
		{"revealed but no id", true, "backend", "", "backend"},
		{"revealed blank label still id", true, "", "a1", "a1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			showIDs = tc.ids
			if got := agentDisplay(tc.label, tc.id); got != tc.want {
				t.Errorf("agentDisplay(%q, %q) [ids=%v] = %q, want %q", tc.label, tc.id, tc.ids, got, tc.want)
			}
		})
	}
}

// TestCLIAgentLsNameFirst asserts `dejima agent ls` is NAME-led and label-only by
// default (no ID column), and that --ids reveals the id column after the name.
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

	headerOf := func(out string) string {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			return out[:i]
		}
		return out
	}

	// Default: NAME-led, the label shows, and there is NO ID column.
	out, err := runCLI(t, "agent", "ls", "proj")
	if err != nil {
		t.Fatalf("agent ls: %v", err)
	}
	header := headerOf(out)
	if !strings.Contains(header, "NAME") {
		t.Errorf("agent ls header should lead with NAME: %q", header)
	}
	if strings.Contains(header, "ID") {
		t.Errorf("agent ls default should NOT show an ID column: %q", header)
	}
	if !strings.Contains(out, "backend") {
		t.Errorf("agent ls should show the agent's label %q: %q", "backend", out)
	}

	// --ids: the id column appears, after NAME.
	out, err = runCLI(t, "agent", "ls", "proj", "--ids")
	if err != nil {
		t.Fatalf("agent ls --ids: %v", err)
	}
	header = headerOf(out)
	if ni, idi := strings.Index(header, "NAME"), strings.Index(header, "ID"); ni < 0 || idi < 0 || ni > idi {
		t.Errorf("--ids: NAME should come before ID in the header: %q", header)
	}
}
