package api

import (
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/providercreds"
)

// TestAgentProviderStatus checks the proactive missing-provider-auth computation
// against the handler registry + provider store (no logs involved).
func TestAgentProviderStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the provider store

	// A non-LLM agent never reports an auth problem.
	if _, _, keySet, auth := agentProviderStatus(&project.AgentSpec{Type: "claude-code"}); auth != "" || keySet {
		t.Errorf("claude-code: got keySet=%v auth=%q, want false/\"\"", keySet, auth)
	}

	// A key-requiring agent with no stored key is flagged.
	oc := &project.AgentSpec{Type: "openclaw", Provider: "anthropic", Model: "anthropic/claude-sonnet-4-6"}
	prov, model, keySet, auth := agentProviderStatus(oc)
	if auth != "missing-provider-auth" || keySet {
		t.Errorf("openclaw w/o key: got keySet=%v auth=%q, want false/missing-provider-auth", keySet, auth)
	}
	if prov != "anthropic" || model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("openclaw echo: provider=%q model=%q", prov, model)
	}

	// Once a matching key is stored, it reads ready.
	if _, err := providercreds.Update(func(s *providercreds.Store) error {
		s.Put(providercreds.Provider{Name: "anthropic", APIKey: "sk-ant-test"})
		return nil
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, _, keySet, auth := agentProviderStatus(oc); auth != "" || !keySet {
		t.Errorf("openclaw w/ key: got keySet=%v auth=%q, want true/\"\"", keySet, auth)
	}
}
