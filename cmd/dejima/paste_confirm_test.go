package main

import (
	"strings"
	"testing"
)

func TestPasteUploadEnabled(t *testing.T) {
	for _, off := range []string{"off", "none", "0", "false", "no", "OFF"} {
		t.Setenv("DEJIMA_PASTE_UPLOAD", off)
		if pasteUploadEnabled() {
			t.Errorf("DEJIMA_PASTE_UPLOAD=%q should disable auto-upload", off)
		}
	}
	for _, on := range []string{"", "on", "1", "yes", "anything"} {
		t.Setenv("DEJIMA_PASTE_UPLOAD", on)
		if !pasteUploadEnabled() {
			t.Errorf("DEJIMA_PASTE_UPLOAD=%q should keep auto-upload on", on)
		}
	}
}

func TestPasteDropPolicy(t *testing.T) {
	t.Setenv("DEJIMA_PASTE_UPLOAD", "") // enabled

	// Plain shell (not alt-screen) → confirm.
	if got := pasteDropPolicy(false); got != pasteConfirm {
		t.Errorf("shell + enabled → %v, want pasteConfirm", got)
	}
	// Full-screen TUI → text (can't safely prompt over its screen).
	if got := pasteDropPolicy(true); got != pasteAsText {
		t.Errorf("alt-screen → %v, want pasteAsText", got)
	}
	// Disabled → always text, even in a shell.
	t.Setenv("DEJIMA_PASTE_UPLOAD", "off")
	if got := pasteDropPolicy(false); got != pasteAsText {
		t.Errorf("disabled → %v, want pasteAsText", got)
	}
}

// TestProcessDropDeclinedForwardsText: when onDrop returns false, the scanner
// forwards the original bracketed paste verbatim (as text) — nothing swallowed.
func TestProcessDropDeclinedForwardsText(t *testing.T) {
	// Use this test file's own path as a guaranteed-existing regular file.
	path := "paste_confirm_test.go"
	pasted := bpStart + path + bpEnd

	var declined bool
	s := &pasteScanner{}
	out := s.process([]byte(pasted),
		func(localPath string, bracketed []byte) bool {
			declined = true
			if localPath != path {
				t.Errorf("onDrop localPath = %q, want %q", localPath, path)
			}
			if string(bracketed) != pasted {
				t.Errorf("onDrop bracketed = %q, want the original paste %q", bracketed, pasted)
			}
			return false // decline → forward as text
		}, nil)

	if !declined {
		t.Fatal("onDrop should have been called for an existing file path")
	}
	if string(out) != pasted {
		t.Errorf("declined drop should forward the paste verbatim, got %q want %q", out, pasted)
	}
}

// TestProcessDropConsumedSwallows: when onDrop returns true, the paste is
// swallowed (not forwarded).
func TestProcessDropConsumedSwallows(t *testing.T) {
	path := "paste_confirm_test.go"
	s := &pasteScanner{}
	out := s.process([]byte(bpStart+path+bpEnd),
		func(string, []byte) bool { return true }, nil)
	if strings.Contains(string(out), path) {
		t.Errorf("consumed drop must not forward the path, got %q", out)
	}
}
