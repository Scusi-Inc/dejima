package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestAgentDisplayIn resolves an agent id to its label within an island the
// operator can see, falling back to the id when unknown (containment / not yet
// polled). id stays the addressing handle.
func TestAgentDisplayIn(t *testing.T) {
	m := tuiModel{islands: []api.IslandInfo{
		{Name: "janus", Agents: []api.AgentInfo{{ID: "j2", Label: "planning"}, {ID: "j1"}}},
	}}
	if got := m.agentDisplayIn("janus", "j2"); got != "planning" {
		t.Errorf("labelled agent = %q, want planning", got)
	}
	if got := m.agentDisplayIn("janus", "j1"); got != "j1" {
		t.Errorf("unlabelled agent should fall back to id, got %q", got)
	}
	if got := m.agentDisplayIn("janus", "x9"); got != "x9" {
		t.Errorf("unknown agent should fall back to id, got %q", got)
	}
	if got := m.agentDisplayIn("nope", "j2"); got != "j2" {
		t.Errorf("unknown island should fall back to id, got %q", got)
	}
}
