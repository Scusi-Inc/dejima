package main

import (
	"os/exec"
	"strings"
	"testing"
)

// dockerErrText must surface the daemon's stderr (where "Cannot connect…" /
// "permission denied" live) so checkDocker can classify the failure — not just
// the opaque "exit status 1".
func TestDockerErrText_PrefersStderr(t *testing.T) {
	// A real failed command whose stderr we can predict, captured via .Output()
	// exactly like checkDocker's `docker version` call (Output populates
	// ExitError.Stderr; Run does not).
	cmd := exec.Command("sh", "-c", "echo 'permission denied while trying to connect' >&2; exit 1")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("expected the command to fail")
	}
	got := dockerErrText(err)
	if !strings.Contains(strings.ToLower(got), "permission denied") {
		t.Fatalf("dockerErrText should expose stderr; got %q", got)
	}
}

func TestDockerErrText_FallsBackToErrString(t *testing.T) {
	// A plain error (no ExitError.Stderr) should fall back to err.Error().
	got := dockerErrText(exec.ErrNotFound)
	if got == "" {
		t.Fatal("dockerErrText returned empty for a non-ExitError")
	}
}

// The per-OS hints must always offer a concrete next step (never empty).
func TestDockerHintsNonEmpty(t *testing.T) {
	if dockerInstallHint() == "" {
		t.Error("dockerInstallHint is empty")
	}
	if dockerPermHint() == "" {
		t.Error("dockerPermHint is empty")
	}
}
