package main

import (
	"strings"
	"testing"
)

// A typed confirmation that doesn't match used to fall through to a bare
// `return m, nil`: the prompt closed, nothing ran, and nothing was said. The
// operator saw the agent still sitting there and reasonably concluded delete
// was broken. Not-confirmed has to be distinguishable from done.
func TestUnmatchedConfirmReportsWhatWasExpected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt confirmPrompt
		want   []string
	}{
		{
			name:   "agent id mistyped",
			prompt: confirmPrompt{verb: "remove-agent", island: "wildfire", agent: "w1", answer: "claude"},
			want:   []string{"w1", "claude"}, // what was needed, and what they typed
		},
		{
			name:   "island name mistyped",
			prompt: confirmPrompt{verb: "purge", island: "wildfire", answer: "wildfyre"},
			want:   []string{"wildfire", "wildfyre"},
		},
		{
			name:   "y/n gate answered with something else",
			prompt: confirmPrompt{verb: "reset", island: "wildfire", answer: "sure"},
			want:   []string{`"y"`, "sure"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tuiModel{dirtyOps: map[string]string{}}
			out, _ := m.runConfirmed(tc.prompt)
			got := out.(tuiModel).lastError
			if got == "" {
				t.Fatal("unmatched confirmation reported nothing — silently doing nothing is the bug")
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("message missing %q; got %q", w, got)
				}
			}
		})
	}
}

// An empty answer is a cancel, not a typo — say that, without quoting "".
func TestEmptyConfirmReadsAsCancelled(t *testing.T) {
	m := tuiModel{dirtyOps: map[string]string{}}
	out, _ := m.runConfirmed(confirmPrompt{verb: "remove-agent", island: "isl", agent: "a1", answer: ""})
	got := out.(tuiModel).lastError
	if !strings.Contains(got, "cancelled") {
		t.Errorf("empty answer should read as cancelled; got %q", got)
	}
}

// Verbs whose typed text is data rather than a gate (a new label, a deny
// reason) always act — they must not be reported as failures.
func TestFreeTextConfirmsAreNotReportedAsUnconfirmed(t *testing.T) {
	for _, verb := range []string{"relabel-agent", "rename-island", "deny-action"} {
		if got := confirmExpectation(confirmPrompt{verb: verb}); got != "" {
			t.Errorf("%s takes free text; should have no expectation, got %q", verb, got)
		}
	}
}
