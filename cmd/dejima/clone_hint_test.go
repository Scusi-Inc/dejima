package main

import (
	"strings"
	"testing"
)

func TestCloneFailureHint(t *testing.T) {
	// NB: assert on prose substrings, not the embedded `dejima <cmd>` strings — the
	// coverage gate scans test files and would read a command token here as
	// "that command is now tested".
	cases := []struct{ reason, contains string }{
		{"auth", "authenticate to the git remote"},
		{"not-found", "token with access"},
		{"error", "couldn't clone the repo"},
		{"some-future-reason", "clone failed (some-future-reason)"}, // unknown → generic, not empty
	}
	for _, tc := range cases {
		got := cloneFailureHint("isl", tc.reason)
		if got == "" {
			t.Errorf("cloneFailureHint(isl, %q) must not be empty", tc.reason)
		}
		if !strings.Contains(got, tc.contains) {
			t.Errorf("cloneFailureHint(isl, %q) = %q, want it to contain %q", tc.reason, got, tc.contains)
		}
		if !strings.Contains(got, "isl") {
			t.Errorf("hint should name the island, got %q", got)
		}
	}
	// A blank reason (shouldn't happen, but be safe) still degrades gracefully.
	if cloneFailureHint("isl", "") == "" {
		t.Error("a blank reason must still yield a non-empty hint")
	}
}
