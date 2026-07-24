package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/reposrc"
)

// keyCreator builds a model mid-create at the agent-name step, with a
// key-requiring agent (openclaw) chosen and NO key configured.
func keyCreator(t *testing.T) tuiModel {
	t.Helper()
	return tuiModel{
		agentKeyGap:    map[string]bool{"openclaw": true},
		agentProviders: map[string][]string{"openclaw": {"anthropic", "openai", "google"}},
		creator: &creatorModel{
			step:         stepAgentName,
			pendingAgent: api.AgentSpecRequest{Type: "openclaw"},
			resolution:   reposrc.Resolution{Repo: "r"},
		},
	}
}

// The whole point: a key-requiring agent with no key routes THROUGH the guided
// key step before the roster — so it launches with a key instead of coming up
// broken (the OpenClaw "No API key found" case).
func TestCreateGuidesProviderKeyBeforeRoster(t *testing.T) {
	m := keyCreator(t)

	// Enter on the name step (blank name) → should land on the key step, not the
	// roster, because openclaw needs a key and none is set.
	m = feedCreator(m, "enter")
	if m.creator.step != stepAgentKey {
		t.Fatalf("after naming a keyless key-requiring agent: step=%v, want stepAgentKey", m.creator.step)
	}
	if len(m.creator.keyProviders) != 3 {
		t.Errorf("key step should offer the agent's providers, got %v", m.creator.keyProviders)
	}

	// Type a key; it must be masked in the view, never echoed.
	m = feedCreator(m, "s", "k", "-", "t", "e", "s", "t")
	body := m.creator.view(80)
	if strings.Contains(body, "sk-test") {
		t.Errorf("the key is visible in the pane:\n%s", body)
	}
	if !strings.Contains(body, strings.Repeat("•", 7)) {
		t.Errorf("key should render as 7 bullets:\n%s", body)
	}
}

// An agent whose key is already configured must NOT be interrupted — it goes
// straight to the roster.
func TestCreateSkipsKeyStepWhenSatisfied(t *testing.T) {
	m := keyCreator(t)
	m.agentKeyGap = map[string]bool{} // key already set → no gap

	m = feedCreator(m, "enter")
	if m.creator.step != stepAgents {
		t.Errorf("a satisfied agent should skip the key step: step=%v, want stepAgents", m.creator.step)
	}
}

// A non-key-requiring agent (shell/claude) is never interrupted.
func TestCreateNoKeyStepForOrdinaryAgent(t *testing.T) {
	m := tuiModel{
		creator: &creatorModel{
			step:         stepAgentName,
			pendingAgent: api.AgentSpecRequest{Type: "shell"},
			resolution:   reposrc.Resolution{Repo: "r"},
		},
	}
	m = feedCreator(m, "enter")
	if m.creator.step != stepAgents {
		t.Errorf("shell agent should go straight to the roster: step=%v", m.creator.step)
	}
}

// esc on the key step skips it (proceed without a key) — we guide, not force.
func TestKeyStepSkippable(t *testing.T) {
	m := keyCreator(t)
	m = feedCreator(m, "enter") // → key step
	if m.creator.step != stepAgentKey {
		t.Fatalf("expected key step, got %v", m.creator.step)
	}
	m = feedCreator(m, "esc")
	if m.creator.step != stepAgents {
		t.Errorf("esc should skip to the roster: step=%v", m.creator.step)
	}
}
