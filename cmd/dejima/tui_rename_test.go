package main

import "testing"

// TestRenameNotice: the daemon auto-increments a colliding agent label and
// returns the final one; renameNotice surfaces that only when it actually
// changed (case-insensitively), and never for an empty request.
func TestRenameNotice(t *testing.T) {
	if got := renameNotice("build", "build-2"); got != "'build' was taken — named it build-2" {
		t.Errorf("collision notice = %q", got)
	}
	if got := renameNotice("build", "build"); got != "" {
		t.Errorf("no-collision should be silent, got %q", got)
	}
	if got := renameNotice("Build", "Build"); got != "" {
		t.Errorf("identical (case) should be silent, got %q", got)
	}
	if got := renameNotice("", "anything"); got != "" {
		t.Errorf("empty request is never deduped, got %q", got)
	}
	if got := renameNotice("api", "api-3"); got == "" {
		t.Error("api→api-3 should surface a notice")
	}
}
