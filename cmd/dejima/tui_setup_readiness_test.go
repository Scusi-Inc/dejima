package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// The setup fetch must populate ALL THREE maps — keyGap, gatewayPort, providers.
// A regression left gatewayPort and providers empty (only keyGap was filled),
// which silently disabled "Enter opens the gateway console" and the guided-key
// provider picker across the whole TUI.
func TestApplyAgentTypeReadinessPopulatesAllMaps(t *testing.T) {
	msg := &setupReadinessMsg{
		keyGap:      map[string]bool{},
		gatewayPort: map[string]int{},
		providers:   map[string][]string{},
	}
	types := []api.AgentTypeCapability{
		{Type: "openclaw", RequiresProviderKey: true, GatewayPort: 18789,
			SupportedProviders: []string{"anthropic", "openai", "google"}},
		{Type: "claude-code"}, // ordinary interactive agent: no key, no gateway
	}
	applyAgentTypeReadiness(msg, types, map[string]bool{}) // no keys configured

	if got := msg.gatewayPort["openclaw"]; got != 18789 {
		t.Errorf("gatewayPort[openclaw] = %d, want 18789 (Enter must open the console)", got)
	}
	if got := msg.providers["openclaw"]; len(got) != 3 {
		t.Errorf("providers[openclaw] = %v, want the 3 supported providers (guided-key picker)", got)
	}
	if !msg.keyGap["openclaw"] {
		t.Error("keyGap[openclaw] should be true when no provider key is set")
	}
	// The interactive agent contributes to none of the maps.
	if _, ok := msg.gatewayPort["claude-code"]; ok {
		t.Error("claude-code should have no gateway port")
	}
}

// A configured provider key clears the gap; the gateway port is still reported.
func TestApplyAgentTypeReadinessKeySatisfied(t *testing.T) {
	msg := &setupReadinessMsg{
		keyGap:      map[string]bool{},
		gatewayPort: map[string]int{},
		providers:   map[string][]string{},
	}
	types := []api.AgentTypeCapability{
		{Type: "openclaw", RequiresProviderKey: true, GatewayPort: 18789,
			SupportedProviders: []string{"anthropic", "openai", "google"}},
	}
	applyAgentTypeReadiness(msg, types, map[string]bool{"openai": true})

	if msg.keyGap["openclaw"] {
		t.Error("keyGap[openclaw] should be false once an openai key is configured")
	}
	if msg.gatewayPort["openclaw"] != 18789 {
		t.Error("gatewayPort must be reported regardless of key state")
	}
}
