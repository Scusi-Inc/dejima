package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/handlers"
)

// Every agent type the daemon can run must be offered where agents are added.
//
// This is the gate for a bug that shipped: the picker was a hand-written list,
// `aider` was added to the registry as the local-model pairing and never
// reached it, and the TUI — the way people actually add agents — could not
// offer the one type that can drive a local model. letta, hermes and goose were
// missing for the same reason. A list that mirrors a registry by hand goes
// stale silently, so the mirror is now derived and this holds it to the
// registry.
func TestAgentPickerOffersEveryRegistryType(t *testing.T) {
	offered := map[string]bool{}
	for _, o := range agentTypeOptions {
		offered[o.typ] = true
	}
	for _, h := range handlers.All() {
		if !offered[h.ID] {
			t.Errorf("agent type %q is in the handler registry but not offered in the picker — "+
				"it can only be added with `dejima agent add --type %s`", h.ID, h.ID)
		}
	}
}

// The sharper statement of the original bug: a local model is useless without
// an agent that can drive it, so the local-capable types specifically must be
// reachable from the picker.
func TestAgentPickerOffersLocalCapableTypes(t *testing.T) {
	offered := map[string]bool{}
	for _, o := range agentTypeOptions {
		offered[o.typ] = true
	}
	var found int
	for _, h := range handlers.All() {
		if !supportsLocalProvider(h) {
			continue
		}
		found++
		if !offered[h.ID] {
			t.Errorf("%q supports the local provider but is not offered in the picker — "+
				"an operator who pulled a local model has no way to attach an agent to it", h.ID)
		}
	}
	if found == 0 {
		// Without this the test passes vacuously the day the local provider is
		// renamed, which is exactly when it should fail.
		t.Fatal("no registry type declares the local provider — the test has lost its subject")
	}
}

// Having pulled a local model should land the cursor on the agent that can use
// it, so the operator need not know the framework's name.
func TestAgentPickerDefaultsToLocalCapableWhenModelPulled(t *testing.T) {
	p := newAgentPickerFor(true)
	h, ok := handlers.Lookup(p.typ())
	if !ok || !supportsLocalProvider(h) {
		t.Errorf("with a local model pulled the picker starts on %q, which cannot drive it", p.typ())
	}
	// Without one, nothing is presumed: the cursor stays where it always was.
	if got := newAgentPickerFor(false).cursor; got != 0 {
		t.Errorf("with no local model the picker starts at %d, want 0", got)
	}
}

// Only the generic headless type prompts for a command line. The named
// frameworks are KindHeadless in the registry but launch themselves, so
// deriving `headless` from Kind would ask the operator for a command that is
// then ignored.
func TestAgentPickerPromptsForCommandOnlyForGenericHeadless(t *testing.T) {
	for _, o := range agentTypeOptions {
		if want := o.typ == api.AgentHeadless; o.headless != want {
			t.Errorf("option %q: headless=%v, want %v", o.typ, o.headless, want)
		}
	}
}
