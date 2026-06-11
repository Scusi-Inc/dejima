package api

import "testing"

// TestAgentStateMountTarget guards the handler-registry refactor: the mount
// target must match the pre-refactor hardcoded switch exactly.
func TestAgentStateMountTarget(t *testing.T) {
	cases := []struct {
		agent string
		want  string
	}{
		{"codex", "/home/dejima/.codex"},
		{"claude-code", "/home/dejima/.claude"},
		{"", "/home/dejima/.claude"}, // empty defaults to claude-code
		{"headless", "/home/dejima/.agent-state"},
		{"some-custom-agent", "/home/dejima/.agent-state"}, // unknown fallback
	}
	for _, c := range cases {
		if got := agentStateMountTarget(c.agent); got != c.want {
			t.Errorf("agentStateMountTarget(%q) = %q, want %q", c.agent, got, c.want)
		}
	}
}
