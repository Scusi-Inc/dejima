package localmodel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubBrew controls whether a usable Homebrew appears to exist, and as whom.
//
// Every branch is driven explicitly. Reading the real host would mean the
// brew-present path never runs anywhere the suite runs (no Mac, no brew on CI),
// so the branch that now does the work would be exercised by nothing — the
// shape that let the macOS refusal stand unchallenged in the first place.
func stubBrew(t *testing.T, path string, present bool, uid int) {
	t.Helper()
	pf, gf := findBrew, geteuid
	t.Cleanup(func() { findBrew, geteuid = pf, gf })
	findBrew = func() (string, bool) { return path, present }
	geteuid = func() int { return uid }
}

// macOS with Homebrew: the daemon installs it ITSELF. No sudo, no terminal.
//
// The refusal this replaces was correct about the OFFICIAL installer — it copies
// an .app and then sudos to link the CLI, which dies on "a terminal is required
// to read the password" — and was applied to the whole platform. Homebrew needs
// no sudo; it REFUSES to run under it and installs into a user-owned prefix. So
// the message was telling operators to run, by hand, the command the daemon
// could have run for them.
func TestInstallOnDarwinUsesHomebrewWhenItCan(t *testing.T) {
	// /bin/true stands in for brew so the real code path runs end to end against
	// an inert binary, rather than spawning a shell that gropes for a Homebrew
	// this machine does not have.
	stubBrew(t, "/bin/true", true, 501)
	rc, err := NewOllama().installOn(context.Background(), "darwin")
	if err != nil {
		t.Fatalf("a Mac with Homebrew and a non-root daemon must install, not refuse: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
}

// What the install actually RUNS, asserted separately from whether it refused.
//
// There is no Mac in CI, so "did not refuse" is the most an end-to-end test can
// say — and it says the same thing about a script that installs nothing. The
// script is built by a pure function precisely so this can be checked.
func TestTheDarwinInstallScriptInstallsAndStarts(t *testing.T) {
	script := darwinBrewScript("/opt/homebrew/bin/brew")
	for _, want := range []string{
		"install ollama",        // the install itself
		"services start ollama", // and it should end up RUNNING, not merely present
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the install script does not %q:\n%s", want, script)
		}
	}
	// The brew path is interpolated, so it must be quoted: a launchd daemon's
	// prefix is /opt/homebrew, but nothing stops a host from having one with a
	// space in it, and an unquoted path would silently split.
	if !strings.Contains(script, `"/opt/homebrew/bin/brew"`) {
		t.Errorf("the brew path is not quoted, so a path with a space splits:\n%s", script)
	}
	// Starting is NOT this script's job any more. `brew services` is attempted
	// because it survives a reboot, but the reliable start is Start(), which uses
	// setsid and waits for an answer. See TestTheInstallScriptDoesNotBackgroundAServer.
	//
	// No sudo. That is the entire premise: Homebrew refuses to run under it, and
	// the daemon has no terminal to type a password at.
	if strings.Contains(script, "sudo") {
		t.Errorf("the install uses sudo, which is the thing that cannot work from a "+
			"daemon with no controlling terminal:\n%s", script)
	}
}

// No Homebrew: the refusal stands, and still says the way out.
func TestInstallOnDarwinWithoutBrewStillRefusesActionably(t *testing.T) {
	stubBrew(t, "", false, 501)
	_, err := NewOllama().installOn(context.Background(), "darwin")
	if !errors.Is(err, ErrInstallNeedsTerminal) {
		t.Fatalf("with no brew there is nothing to drive; expected the actionable refusal, got %v", err)
	}
	// An error that only says "no" leaves them exactly as stuck.
	for _, want := range []string{"brew install ollama", "dejima local install", "ollama.com/download"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the way out is not stated — missing %q in:\n%s", want, err)
		}
	}
	// And it must say WHERE. An operator reading this on Windows, connected to a
	// remote Mac daemon, has to run it on the daemon host — "this Mac" is not the
	// machine they are typing on.
	if !strings.Contains(err.Error(), "DAEMON HOST") {
		t.Errorf("the refusal does not say which machine to run it on:\n%s", err)
	}
}

// A daemon running as root must NOT attempt brew: Homebrew exits immediately
// with "Don't run this as root!", which would replace a clear message with a
// confusing one.
func TestInstallOnDarwinAsRootDoesNotAttemptBrew(t *testing.T) {
	stubBrew(t, "/opt/homebrew/bin/brew", true, 0)
	_, err := NewOllama().installOn(context.Background(), "darwin")
	if !errors.Is(err, ErrInstallNeedsTerminal) {
		t.Fatalf("a root daemon must refuse rather than run a brew that will reject it, got %v", err)
	}
}

// Linux has no such problem: the official script is unattended there.
func TestInstallOnLinuxStillRunsTheScript(t *testing.T) {
	rc, err := NewOllama().installOn(context.Background(), "linux")
	if err != nil {
		t.Fatalf("linux install should still shell out: %v", err)
	}
	_ = rc.Close()
}

// These methods run in the DAEMON. A system LaunchDaemon inherits a bare
// /usr/bin:/bin:/usr/sbin:/sbin, so `brew install ollama` — the very thing the
// macOS refusal above tells the operator to run — is invisible to exec.LookPath.
// Detect would then report "not installed" for a correct install, forever.
func TestResolveExeFindsAnInstallThatIsNotOnPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ollama")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// PATH deliberately holds nothing: the binary is only findable via the
	// known-locations list, exactly as it is under launchd.
	t.Setenv("PATH", dir+"-empty")
	orig := ollamaKnownPaths
	ollamaKnownPaths = []string{bin}
	t.Cleanup(func() { ollamaKnownPaths = orig })

	got, ok := NewOllama().resolveExe()
	if !ok {
		t.Fatal("an installed-but-off-PATH backend reports as not installed")
	}
	if got != bin {
		t.Errorf("resolveExe = %q, want %q", got, bin)
	}
}

// The inverse must stay true, or "not installed" stops meaning anything.
func TestResolveExeReportsAGenuinelyMissingBackend(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	orig := ollamaKnownPaths
	ollamaKnownPaths = []string{filepath.Join(t.TempDir(), "nope")}
	t.Cleanup(func() { ollamaKnownPaths = orig })

	if _, ok := NewOllama().resolveExe(); ok {
		t.Error("no binary anywhere, but resolveExe claims it is installed")
	}
}

// A directory named `ollama`, or a non-executable file, is not an install.
func TestResolveExeIgnoresNonExecutables(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "ollama")
	if err := os.WriteFile(plain, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	orig := ollamaKnownPaths
	ollamaKnownPaths = []string{dir, plain}
	t.Cleanup(func() { ollamaKnownPaths = orig })

	if _, ok := NewOllama().resolveExe(); ok {
		t.Error("a non-executable file counted as an install")
	}
}
