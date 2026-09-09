package main

import (
	"strings"
	"testing"
)

// The agent error is the one field in the detail pane carrying a REMEDY, and
// truncate(err, 50) cut it off before the verb — keeping the complaint and
// dropping the fix, which is the worse half to lose.
func TestAgentErrorKeepsTheRemedy(t *testing.T) {
	err := "the codex binary in this island cannot run (`codex --version` exited 1): " +
		"Error: Missing optional dependency @openai/codex-linux-arm64.\n" +
		"Reinstalling it in the island did not fix it either. Rebuild the image:\n" +
		"  dejima image build && dejima upgrade proj"

	lines := agentErrorLines(err)
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "dejima image build") {
		t.Errorf("the remedy did not survive rendering:\n%s", joined)
	}
	if !strings.Contains(joined, "cannot run") {
		t.Errorf("the cause did not survive rendering:\n%s", joined)
	}
	for _, ln := range lines {
		if len(ln) > agentErrorWidth+4 { // +slack for the " …" marker
			t.Errorf("line overflows the pane (%d): %q", len(ln), ln)
		}
	}
}

// Bounded, and visibly so: a runaway error must not push the pane off screen,
// and a silent cut is indistinguishable from a short error.
func TestAgentErrorIsBoundedAndSaysSo(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("some fairly wordy failure detail ", 60))
	lines := agentErrorLines(long)
	if len(lines) > agentErrorLinesMax {
		t.Errorf("rendered %d lines, cap is %d", len(lines), agentErrorLinesMax)
	}
	if !strings.HasSuffix(lines[len(lines)-1], "…") {
		t.Errorf("truncation is unmarked, so a cut error reads as a complete one: %q",
			lines[len(lines)-1])
	}
}

// A long unbroken token (a path, a URL) stays whole — a broken one cannot be
// copied, which defeats the point of showing it.
func TestAgentErrorKeepsLongTokensWhole(t *testing.T) {
	url := "https://example.com/a/very/long/path/that/exceeds/the/pane/width/by/quite/a/lot"
	lines := agentErrorLines("see " + url)
	if !strings.Contains(strings.Join(lines, " "), url) {
		t.Errorf("the token was broken up:\n%v", lines)
	}
}

// Empty in, empty out — no stray "error:" row for an agent with no error.
func TestAgentErrorEmpty(t *testing.T) {
	if got := agentErrorLines("   \n  "); len(got) != 0 {
		t.Errorf("expected no lines, got %v", got)
	}
}
