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

// A remote-target unreachable diagnosis must reassure (work is safe on the
// server), promise the automatic retry, and offer the tailnet check + client
// reinstall + ask-the-operator escape hatch — the calm recovery path a teammate
// on a phone needs, not the local "start dejimad" steps.
func TestDiagnoseRemoteDaemon(t *testing.T) {
	d := diagnoseRemoteDaemon("mac-mini:7273")
	if !d.Remote {
		t.Fatalf("remote diagnosis must set Remote=true")
	}
	if !strings.Contains(d.Cause, "safe") || !strings.Contains(d.Cause, "mac-mini:7273") {
		t.Fatalf("remote cause should reassure and name the host; got %q", d.Cause)
	}
	if !hasStepContaining(d.Steps, "retries automatically") {
		t.Errorf("remote steps should mention the automatic retry; got %v", d.Steps)
	}
	if !hasStepContaining(d.Steps, "tailscale status") || !hasStepContaining(d.Steps, "tailscale ping mac-mini") {
		t.Errorf("remote steps should offer the tailnet check with a bare host; got %v", d.Steps)
	}
	if !hasStepContaining(d.Steps, "install-client") {
		t.Errorf("remote steps should offer a client reinstall; got %v", d.Steps)
	}
	if !hasStepContaining(d.Steps, "operator") {
		t.Errorf("remote steps should offer the ask-the-operator fallback; got %v", d.Steps)
	}
	// The render must carry the reassurance + the auto-retry closing line, and must
	// NOT tell a remote user to run commands "on the host shell" (that's the local
	// footer).
	out := renderDaemonHelp(d)
	if !strings.Contains(out, "safe") {
		t.Errorf("remote render should keep the reassurance; got:\n%s", out)
	}
	if strings.Contains(out, "on the host shell") {
		t.Errorf("remote render must not use the local host-shell footer; got:\n%s", out)
	}
}

// An empty host (profile with no host recorded) must still render without a bare
// dangling `tailscale ping` — it falls back to a placeholder.
func TestDiagnoseRemoteDaemon_EmptyHost(t *testing.T) {
	d := diagnoseRemoteDaemon("")
	if !strings.Contains(d.Cause, "the server") {
		t.Fatalf("empty-host cause should fall back to \"the server\"; got %q", d.Cause)
	}
	if !hasStepContaining(d.Steps, "tailscale ping <server>") {
		t.Errorf("empty-host should render a ping placeholder; got %v", d.Steps)
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
