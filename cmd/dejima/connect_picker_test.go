package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestChooseAgent covers the multi-agent attach picker: bare Enter takes the
// first agent, a valid number takes that agent, and out-of-range/garbage errors
// with a Ctrl-C-to-cancel hint. Off a TTY it can't prompt and errors with the
// shorthand instead.
func TestChooseAgent(t *testing.T) {
	agents := []api.AgentInfo{
		{ID: "a1", Label: "claude"},
		{ID: "a2", Label: "claude-2"},
	}

	cases := []struct {
		name    string
		input   string
		wantID  string
		wantErr string // substring; "" means no error
	}{
		{"enter takes first", "\n", "a1", ""},
		{"whitespace takes first", "   \n", "a1", ""},
		{"pick second", "2\n", "a2", ""},
		{"out of range", "9\n", "", "isn't one of the choices"},
		{"not a number", "yes\n", "", "isn't one of the choices"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := chooseAgent("lincoln_analysis-2", agents, strings.NewReader(c.input), true)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if id != c.wantID {
				t.Errorf("chose %q, want %q", id, c.wantID)
			}
		})
	}

	// Non-interactive (no TTY): can't prompt — errors pointing at the island/agent
	// shorthand rather than hanging on a read.
	if _, err := chooseAgent("lincoln_analysis-2", agents, strings.NewReader(""), false); err == nil ||
		!strings.Contains(err.Error(), "multiple agents") {
		t.Errorf("non-interactive err = %v, want a 'multiple agents' hint", err)
	}
}
