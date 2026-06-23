package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withDejimaHome points paths.* at a throwaway HOME so the socket-state probe in
// diagnoseLocalDaemon sees exactly what the test sets up.
func withDejimaHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".dejima")
}

func TestDiagnoseLocalDaemon_SocketMissing(t *testing.T) {
	withDejimaHome(t) // no ~/.dejima/dejimad.sock created

	d := diagnoseLocalDaemon()
	if !strings.Contains(d.Cause, "doesn't exist yet") {
		t.Fatalf("missing-socket cause should say the socket doesn't exist; got %q", d.Cause)
	}
	if !hasStepContaining(d.Steps, "dejimad --foreground") {
		t.Errorf("missing-socket steps should offer a foreground start; got %v", d.Steps)
	}
	if !hasStepContaining(d.Steps, "dejima service install") {
		t.Errorf("missing-socket steps should offer install; got %v", d.Steps)
	}
}

func TestDiagnoseLocalDaemon_SocketStale(t *testing.T) {
	dir := withDejimaHome(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A regular file standing in for a stale socket: it exists, so the probe must
	// classify this as stopped/crashed rather than never-installed.
	if err := os.WriteFile(filepath.Join(dir, "dejimad.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	d := diagnoseLocalDaemon()
	if !strings.Contains(d.Cause, "stopped or crashed") {
		t.Fatalf("stale-socket cause should say stopped/crashed; got %q", d.Cause)
	}
	if !hasStepContaining(d.Steps, "doctor") {
		t.Errorf("stopped daemon steps should point at `dejima doctor`; got %v", d.Steps)
	}
}

// compactSteps must drop the empty strings logHint() returns on unmanaged OSes,
// so the rendered list never shows a blank bullet.
func TestCompactStepsDropsEmpty(t *testing.T) {
	got := compactSteps([]string{"a", "", "  ", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("compactSteps did not drop blanks: %v", got)
	}
}

func hasStepContaining(steps []string, sub string) bool {
	for _, s := range steps {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
