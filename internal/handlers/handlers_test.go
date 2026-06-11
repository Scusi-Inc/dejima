package handlers

import "testing"

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
