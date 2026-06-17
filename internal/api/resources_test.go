package api

import "testing"

func TestOOMScoreAdj(t *testing.T) {
	cases := map[int]int{
		100:  -500,  // critical preset → killed last
		0:    0,     // normal
		-100: 500,   // expendable preset → killed first
		300:  -1000, // clamped low
		-300: 1000,  // clamped high
	}
	for prio, want := range cases {
		if got := oomScoreAdj(prio); got != want {
			t.Errorf("oomScoreAdj(%d) = %d, want %d", prio, got, want)
		}
	}
}

func TestResolveOOMPriority(t *testing.T) {
	explicit := 42
	// Explicit value always wins, regardless of agent type.
	if got := resolveOOMPriority(&explicit, "claude-code"); got != 42 {
		t.Errorf("explicit priority = %d, want 42", got)
	}
	// Unset + headless primary → expendable (sacrifice the self-restarting brain).
	if got := resolveOOMPriority(nil, "openclaw"); got != oomPriorityExpendable {
		t.Errorf("headless default = %d, want %d", got, oomPriorityExpendable)
	}
	// Unset + interactive primary → normal (0).
	if got := resolveOOMPriority(nil, "claude-code"); got != 0 {
		t.Errorf("interactive default = %d, want 0", got)
	}
}
