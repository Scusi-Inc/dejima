package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// Pressing Enter on a key-requiring agent that has no key configured
// (AuthState "missing-provider-auth") must route to the provider/model/key
// editor — not open its UI or dump its logs, the dead end operators hit. This
// is the "where's the guidance?" fix: the fix is one keystroke from the error.
func TestEnterOnKeylessAgentOpensKeyEditor(t *testing.T) {
	m := initialTUIModel(nil)
	m.islands = []api.IslandInfo{{
		Name: "home", Container: "running",
		Agents: []api.AgentInfo{
			{ID: "o1", Type: "openclaw", Attachable: false,
				Provider: "openai", Model: "openai/gpt-5.5", AuthState: "missing-provider-auth"},
		},
	}}
	m.gatewayPorts = map[string]int{"openclaw": 18789}
	m.expanded = map[string]bool{"home": true}
	m.selected = 1 // island row is 0; the openclaw agent is row 1

	if r := m.currentRow(); r.agentID != "o1" {
		t.Fatalf("test setup: current row is %+v, want the openclaw agent", r)
	}

	next, _ := m.activateRow()
	tm, ok := next.(tuiModel)
	if !ok {
		t.Fatalf("activateRow returned %T, want tuiModel", next)
	}
	if tm.modelEditor == nil {
		t.Fatal("Enter on a keyless agent should open the model/key editor, but modelEditor is nil")
	}
	if tm.modelEditor.agentID != "o1" {
		t.Errorf("editor targets agent %q, want o1", tm.modelEditor.agentID)
	}
	if tm.lastNotice == "" {
		t.Error("should explain why the editor opened (no API key)")
	}
}

// An agent whose key IS configured is not intercepted — Enter opens it normally
// (here: the gateway UI path, which nudges about the SSH façade when off).
func TestEnterOnConfiguredGatewayAgentNotIntercepted(t *testing.T) {
	m := initialTUIModel(nil)
	m.islands = []api.IslandInfo{{
		Name: "home", Container: "running",
		Agents: []api.AgentInfo{
			{ID: "o1", Type: "openclaw", Attachable: false, Provider: "openai"}, // no auth gap
		},
	}}
	m.gatewayPorts = map[string]int{"openclaw": 18789}
	m.overview = &api.OverviewResponse{} // façade off → gateway open nudges
	m.expanded = map[string]bool{"home": true}
	m.selected = 1

	next, _ := m.activateRow()
	tm := next.(tuiModel)
	if tm.modelEditor != nil {
		t.Error("a configured agent must not be routed to the key editor")
	}
}
