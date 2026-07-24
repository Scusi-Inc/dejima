package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// The provider picker should open on the RIGHT provider. When the agent has an
// explicit Provider, use it; otherwise derive it from the model string
// ("openai/gpt-5.5" → openai) so an openclaw agent doesn't always land on the
// first provider (anthropic) when its model clearly says openai.
func TestModelEditorPreselectsProvider(t *testing.T) {
	cap := api.AgentTypeCapability{
		Type:               "openclaw",
		SupportedProviders: []string{"anthropic", "openai", "google"},
	}

	// No explicit provider, but the model encodes it.
	ed := &modelEditor{model: "openai/gpt-5.5"}
	ed.applyLoaded(modelEditorLoadedMsg{cap: cap, keyStatus: map[string]bool{}, curProvider: ""})
	if got := ed.currentProvider(); got != "openai" {
		t.Errorf("derived provider = %q, want openai (from the model prefix)", got)
	}

	// An explicit provider wins over the model prefix.
	ed = &modelEditor{model: "openai/gpt-5.5"}
	ed.applyLoaded(modelEditorLoadedMsg{cap: cap, keyStatus: map[string]bool{}, curProvider: "anthropic"})
	if got := ed.currentProvider(); got != "anthropic" {
		t.Errorf("explicit provider = %q, want anthropic", got)
	}
}
