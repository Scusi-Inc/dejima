package localmodel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The macOS installer copies an .app and then sudos to link the CLI. These
// methods run in the daemon, which has no controlling terminal, so that sudo
// always dies on "a terminal is required to read the password" — and the
// operator sees a 100% download followed by "ERROR: exit status 1", which reads
// as a network flake. Refuse up front and say what to run instead.
func TestInstallOnDarwinRefusesInsteadOfFailingAtSudo(t *testing.T) {
	_, err := NewOllama().installOn(context.Background(), "darwin")
	if !errors.Is(err, ErrInstallNeedsTerminal) {
		t.Fatalf("macOS install must refuse with the actionable error, got %v", err)
	}
	// An error that only says "no" leaves them exactly as stuck.
	for _, want := range []string{"brew install ollama", "dejima local install", "ollama.com/download"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the way out is not stated — missing %q in:\n%s", want, err)
		}
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
