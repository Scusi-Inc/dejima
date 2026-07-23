package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func gwModel() tuiModel {
	return tuiModel{
		islands: []api.IslandInfo{{
			Name: "home", Agent: "openclaw",
			Agents: []api.AgentInfo{
				{ID: "o1", Type: "openclaw", Attachable: false},
				{ID: "c1", Type: "claude-code", Attachable: true},
			},
		}},
		gatewayPorts: map[string]int{"openclaw": 18789}, // from /v1/agent-types
	}
}

// A gateway agent (OpenClaw) must be recognised as having a UI to open, while a
// regular agent is not — that's what routes Enter to the UI instead of logs.
func TestAgentGatewayPortDetection(t *testing.T) {
	m := gwModel()

	if port, ok := m.agentGatewayPort("home", "o1"); !ok || port != 18789 {
		t.Errorf("openclaw agent: port=%d ok=%v, want 18789/true", port, ok)
	}
	if _, ok := m.agentGatewayPort("home", "c1"); ok {
		t.Error("claude-code agent should have no gateway port")
	}
	// The island's primary (agentID == "") resolves too.
	if port, ok := m.agentGatewayPort("home", ""); !ok || port != 18789 {
		t.Errorf("primary resolution: port=%d ok=%v, want 18789/true", port, ok)
	}
}

// Opening a gateway UI needs the SSH façade. Without it, the TUI must give an
// actionable nudge — not spawn a window that just fails — so the operator knows
// the one thing to enable.
func TestGatewayUIRequiresSSHFacade(t *testing.T) {
	m := gwModel()
	m.overview = &api.OverviewResponse{} // SSHAddr empty → façade off

	out, _ := m.openAgentGatewayUI("home", "o1")
	got := out.(tuiModel).lastError
	if !strings.Contains(got, "SSH façade") {
		t.Errorf("should point at enabling the SSH façade; got %q", got)
	}
	if !strings.Contains(got, "ssh enroll") {
		t.Errorf("should name the enroll step; got %q", got)
	}
}
