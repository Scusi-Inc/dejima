package main

import (
	"strings"
	"testing"
)

// adderFor builds an add-agent overlay on an island, with the key-gap /
// provider metadata the setup-readiness fetch normally supplies.
func adderFor(gap map[string]bool) tuiModel {
	m := initialTUIModel(nil)
	m.agentKeyGap = gap
	m.agentProviders = map[string][]string{"openclaw": {"anthropic", "openai", "google"}}
	m.agentAdder = &agentAdder{island: "myrepo", picker: newAgentPicker(), keyGap: gap}
	return m
}

// pickAgent moves the adder's picker to typ and selects it, deriving the walk
// from agentTypeOptions (see downsTo) rather than counting "down"s against a
// list order copied into a comment.
//
// What that buys here is specific: these tests turn on WHICH agent was picked —
// one with a key gap routes through the key step, one without goes straight to
// the label. A reorder that silently pointed a hardcoded walk at a different
// agent would leave every assertion below passing, about an agent whose key
// requirement is the opposite of the one the test is named for.
func pickAgent(t *testing.T, m tuiModel, typ string) {
	t.Helper()
	for _, k := range append(downsTo(t, typ), "enter") {
		m.agentAdderKey(key(k))
	}
}

// Adding a key-requiring agent with no key set routes through the guided key
// step before the label — so it launches ready, not broken.
func TestAddAgentGuidesProviderKey(t *testing.T) {
	m := adderFor(map[string]bool{"openclaw": true})
	a := m.agentAdder

	pickAgent(t, m, "openclaw")
	if a.phase != adderKey {
		t.Fatalf("openclaw with no key: phase = %v, want adderKey", a.phase)
	}
	if len(a.keyProviders) != 3 {
		t.Errorf("key step should offer the agent's providers, got %v", a.keyProviders)
	}

	// Type a key — masked, never echoed. "k" must land in the field, not navigate.
	for _, c := range []string{"s", "k", "-", "t", "e", "s", "t"} {
		m.agentAdderKey(key(c))
	}
	if a.keyInput != "sk-test" {
		t.Fatalf("keyInput = %q, want sk-test", a.keyInput)
	}
	body := a.view()
	if strings.Contains(body, "sk-test") {
		t.Errorf("the key is visible in the pane:\n%s", body)
	}
	if !strings.Contains(body, strings.Repeat("•", 7)) {
		t.Errorf("key should render as 7 bullets:\n%s", body)
	}

	// esc skips the key step (guide, don't force) → on to the label.
	m.agentAdderKey(key("esc"))
	if a.phase != adderLabel {
		t.Errorf("esc should skip to the label step: phase = %v", a.phase)
	}
}

// An agent whose key is already configured is not interrupted — straight to the
// label step.
func TestAddAgentSkipsKeyWhenSatisfied(t *testing.T) {
	m := adderFor(map[string]bool{}) // no gap → key already set
	a := m.agentAdder

	pickAgent(t, m, "openclaw")
	if a.phase != adderLabel {
		t.Errorf("a satisfied agent should skip the key step: phase = %v, want adderLabel", a.phase)
	}
}

// An ordinary agent (shell) never sees the key step.
func TestAddAgentOrdinaryNoKeyStep(t *testing.T) {
	m := adderFor(map[string]bool{"openclaw": true})
	a := m.agentAdder

	pickAgent(t, m, "shell")
	if a.phase != adderLabel {
		t.Errorf("shell should go straight to the label step: phase = %v", a.phase)
	}
}
