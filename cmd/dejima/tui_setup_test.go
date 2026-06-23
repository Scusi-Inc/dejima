package main

import (
	"strings"
	"testing"
)

// The picker must flag agent types whose required LLM key isn't configured, so
// the operator sees it at pick time — not when the agent fails to authenticate
// after the island already exists.
func TestPickerView_AnnotatesKeyGap(t *testing.T) {
	p := newAgentPicker()

	var with strings.Builder
	p.view(&with, "Agent", map[string]bool{"openclaw": true})
	if !strings.Contains(with.String(), "needs an LLM key") {
		t.Errorf("picker should flag a key-gapped type; got:\n%s", with.String())
	}

	// No gap → no warning noise.
	var without strings.Builder
	p.view(&without, "Agent", nil)
	if strings.Contains(without.String(), "needs an LLM key") {
		t.Errorf("picker should not warn when there's no key gap; got:\n%s", without.String())
	}
}
