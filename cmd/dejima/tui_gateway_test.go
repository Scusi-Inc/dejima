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
//
// It opens the PANE rather than setting lastError, and that is the fix rather
// than a refactor: renderFooterLeft truncates lastError at 60 characters, and
// the steps are ~180, so the operator's actual screen was a red bar cut off
// mid-command. Asserted here as "the pane is open"; its contents are held down
// by TestFacadeGuidance* in ssh_setup_addr_test.go.
func TestGatewayUIRequiresSSHFacade(t *testing.T) {
	m := gwModel()
	m.overview = &api.OverviewResponse{} // SSHAddr empty → façade off

	out, _ := m.openAgentGatewayUI("home", "o1")
	got := out.(tuiModel)
	if !got.sshHelp {
		t.Fatal("no guidance shown for a gateway UI with the façade off")
	}
	if strings.Contains(got.lastError, "service install") {
		t.Error("the steps went to lastError, which the footer truncates at 60 " +
			"chars — the enable command reaches the operator cut in half")
	}
	// The pane the operator will actually read must carry both steps.
	pane := sshFacadeHelpPane("100.89.51.27:7273", true, "sudo dejima service install --ssh x:2222", false)
	for _, want := range []string{"SSH façade", "service install", "SSH setup"} {
		if !strings.Contains(pane, want) {
			t.Errorf("the guidance pane is missing %q", want)
		}
	}
}
