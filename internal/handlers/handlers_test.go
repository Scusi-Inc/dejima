package handlers

import (
	"strings"
	"testing"
)

func TestLookupAndAttachable(t *testing.T) {
	cases := []struct {
		agent      string
		wantOK     bool
		wantKind   Kind
		wantState  string
		attachable bool
	}{
		{"claude-code", true, KindInteractive, "/home/dejima/.claude", true},
		{"codex", true, KindInteractive, "/home/dejima/.codex", true},
		{"headless", true, KindHeadless, "/home/dejima/.agent-state", false},
		{"some-custom-agent", false, "", "", true}, // unknown ⇒ assumed interactive
	}
	for _, c := range cases {
		h, ok := Lookup(c.agent)
		if ok != c.wantOK {
			t.Errorf("Lookup(%q) ok = %v, want %v", c.agent, ok, c.wantOK)
		}
		if ok {
			if h.Kind != c.wantKind {
				t.Errorf("Lookup(%q).Kind = %q, want %q", c.agent, h.Kind, c.wantKind)
			}
			if h.StateDir != c.wantState {
				t.Errorf("Lookup(%q).StateDir = %q, want %q", c.agent, h.StateDir, c.wantState)
			}
		}
		if got := Attachable(c.agent); got != c.attachable {
			t.Errorf("Attachable(%q) = %v, want %v", c.agent, got, c.attachable)
		}
	}
}

func TestHeadlessLaunchIsUserSupplied(t *testing.T) {
	h, _ := Lookup(Headless)
	if h.Launch != "" {
		t.Errorf("headless Launch = %q, want empty (user-supplied)", h.Launch)
	}
}

// TestFrameworkAdapters covers the first-class headless framework adapters
// (Letta / Hermes / Goose): each is headless, key-requiring, self-installing, and
// sources the materialized provider key in its launch. Hermes is a messaging
// bridge with no localhost UI (GatewayPort 0); the other two expose web UIs.
func TestFrameworkAdapters(t *testing.T) {
	cases := []struct {
		agent       string
		wantGateway int
	}{
		{"letta", 8283},
		{"hermes", 0},
		{"goose", 3000},
	}
	for _, c := range cases {
		h, ok := Lookup(c.agent)
		if !ok {
			t.Errorf("%s not registered", c.agent)
			continue
		}
		if h.Kind != KindHeadless {
			t.Errorf("%s Kind = %q, want headless", c.agent, h.Kind)
		}
		if h.Attachable() {
			t.Errorf("%s should not be attachable (it's a headless framework)", c.agent)
		}
		if !h.NeedsProviderKey() {
			t.Errorf("%s should require a provider key", c.agent)
		}
		if h.Launch == "" {
			t.Errorf("%s has no Launch (first-class adapters bake one)", c.agent)
		}
		if !strings.Contains(h.Launch, "DEJIMA_PROVIDER_KEY_FILE") {
			t.Errorf("%s launch must source the provider key file: %q", c.agent, h.Launch)
		}
		if h.GatewayPort != c.wantGateway {
			t.Errorf("%s GatewayPort = %d, want %d", c.agent, h.GatewayPort, c.wantGateway)
		}
		if len(h.SupportedProviders) == 0 {
			t.Errorf("%s should advertise SupportedProviders", c.agent)
		}
	}
}
