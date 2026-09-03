package main

import (
	"errors"
	"strings"
	"testing"
)

// A failed release check must not read as "you're current". The TUI discarded
// the error and kept latestRelease empty, so pressing [U] answered "already up
// to date" while the daemon was two releases behind and the check had actually
// been rate-limited. Not knowing and knowing-you're-current are different
// states and have to stay distinguishable.
func TestLatestReleaseFailureIsRemembered(t *testing.T) {
	m := tuiModel{}

	out, _ := m.Update(latestReleaseMsg{err: errors.New("github API rate limit reached")})
	got := out.(tuiModel)

	if got.updateCheckErr == "" {
		t.Fatal("a failed check left no reason recorded — [U] would claim 'already up to date'")
	}
	if !strings.Contains(got.updateCheckErr, "rate limit") {
		t.Errorf("reason should carry the cause; got %q", got.updateCheckErr)
	}
	if got.latestRelease != "" {
		t.Errorf("a failed check must not invent a latest release; got %q", got.latestRelease)
	}
	if got.clientUpdate || got.daemonUpdate {
		t.Error("a failed check must not claim an update is available")
	}
}

// A later success clears the remembered failure — otherwise a single blip would
// leave the TUI complaining until restart.
func TestLatestReleaseSuccessClearsError(t *testing.T) {
	m := tuiModel{updateCheckErr: "github API rate limit reached"}

	out, _ := m.Update(latestReleaseMsg{latest: "v9.9.9"})
	got := out.(tuiModel)

	if got.updateCheckErr != "" {
		t.Errorf("a successful check should clear the prior failure; got %q", got.updateCheckErr)
	}
	if got.latestRelease != "v9.9.9" {
		t.Errorf("latestRelease = %q, want v9.9.9", got.latestRelease)
	}
}
