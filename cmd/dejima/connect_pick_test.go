package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func agent(id, label string, attachable bool) api.AgentInfo {
	return api.AgentInfo{ID: id, Label: label, Attachable: attachable}
}

// selectAgentFromList is the default-to-agent policy for `dejima connect <island>`:
// filter to attachable agents, then 0 → shell, 1 → that agent, N → choose.
func TestSelectAgentFromList(t *testing.T) {
	cases := []struct {
		name        string
		agents      []api.AgentInfo
		in          string
		interactive bool
		wantID      string
		wantErr     bool
	}{
		{
			name:   "no agents → empty (caller opens the workspace shell)",
			agents: nil,
			wantID: "",
		},
		{
			name:   "non-attachable agents are ignored → empty",
			agents: []api.AgentInfo{agent("a1", "build", false)},
			wantID: "",
		},
		{
			name:   "single attachable agent → attach it, no prompt",
			agents: []api.AgentInfo{agent("a1", "claude", true), agent("a2", "stopped", false)},
			wantID: "a1",
		},
		{
			name:        "multiple → interactive choice",
			agents:      []api.AgentInfo{agent("a1", "backend", true), agent("a2", "frontend", true)},
			in:          "2\n",
			interactive: true,
			wantID:      "a2",
		},
		{
			name:        "multiple → bare Enter takes the first",
			agents:      []api.AgentInfo{agent("a1", "backend", true), agent("a2", "frontend", true)},
			in:          "\n",
			interactive: true,
			wantID:      "a1",
		},
		{
			name:        "multiple → out-of-range is an error",
			agents:      []api.AgentInfo{agent("a1", "backend", true), agent("a2", "frontend", true)},
			in:          "5\n",
			interactive: true,
			wantErr:     true,
		},
		{
			name:        "multiple → non-numeric is an error",
			agents:      []api.AgentInfo{agent("a1", "backend", true), agent("a2", "frontend", true)},
			in:          "nope\n",
			interactive: true,
			wantErr:     true,
		},
		{
			name:    "multiple off a TTY → error listing them (no hang)",
			agents:  []api.AgentInfo{agent("a1", "backend", true), agent("a2", "frontend", true)},
			wantErr: true, // interactive=false
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectAgentFromList("isl", tc.agents, strings.NewReader(tc.in), tc.interactive)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got id %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantID {
				t.Fatalf("id = %q, want %q", got, tc.wantID)
			}
		})
	}
}
