package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/service"
)

// withDejimaHome points paths.* at a throwaway HOME so the socket-state probe in
// diagnoseLocalDaemon sees exactly what the test sets up.
func withDejimaHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".dejima")
}

// The missing-socket remedy forks on how the daemon is supervised, so drive
// diagnosisNotRunning directly with each supervision state.
//
// Testing it through diagnoseLocalDaemon() instead made the result depend on
// whether the machine RUNNING the tests happens to have dejimad installed:
// service.Detect() is a real probe, so on a developer's own host it reported
// "managed" and the test failed against a correct implementation. Setting HOME
// to a temp dir isolates the socket path but not the system service.
func TestDiagnoseNotRunning(t *testing.T) {
	const sock = "/tmp/dejima-test/dejimad.sock"

	t.Run("nothing installed", func(t *testing.T) {
		d := diagnosisNotRunning(service.Supervision{Mode: "none"}, sock)
		if !strings.Contains(d.Cause, "doesn't exist yet") {
			t.Fatalf("cause should say the socket doesn't exist; got %q", d.Cause)
		}
		if !hasStepContaining(d.Steps, "dejimad --foreground") {
			t.Errorf("should offer a foreground start; got %v", d.Steps)
		}
		if !hasStepContaining(d.Steps, "dejima service install") {
			t.Errorf("should offer install; got %v", d.Steps)
		}
	})

	t.Run("managed but no socket", func(t *testing.T) {
		// Registered with a service manager yet no socket: it is crash-looping,
		// so the remedy is logs, not install.
		d := diagnosisNotRunning(service.Supervision{Managed: true, Mode: "launchd-system"}, sock)
		if !strings.Contains(d.Cause, "failing to start") {
			t.Errorf("cause should say it's failing to start; got %q", d.Cause)
		}
		if !hasStepContaining(d.Steps, "dejima logs") && !hasStepContaining(d.Steps, "log") {
			t.Errorf("should point at logs; got %v", d.Steps)
		}
	})

	t.Run("installed but not loaded", func(t *testing.T) {
		// Detect() writes the exact remediation into Concern; it must survive.
		const concern = "sudo dejima service restart --system"
		d := diagnosisNotRunning(service.Supervision{
			Mode: "launchd-system", Summary: "system LaunchDaemon present", Concern: concern,
		}, sock)
		if !strings.Contains(d.Cause, "installed but not loaded") {
			t.Errorf("cause should say installed-but-not-loaded; got %q", d.Cause)
		}
		if !hasStepContaining(d.Steps, concern) {
			t.Errorf("should carry Detect()'s remediation %q; got %v", concern, d.Steps)
		}
	})
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

// The Windows local-target diagnosis. Before this existed, a Windows user whose
// client fell back to "local" got the generic advice — `dejimad --foreground`,
// `dejima service install`, `dejima onboard` — none of which can work there:
// the daemon needs a Unix host with Docker, so the socket it names can never
// appear. The regression to guard is any of those commands coming back.
func TestDiagnoseWindowsClient(t *testing.T) {
	d := diagnosisWindowsClient()

	if !strings.Contains(d.Cause, "Windows can't run the Dejima daemon") {
		t.Errorf("cause should name the platform limit; got %q", d.Cause)
	}
	// The steps must route somewhere that actually works.
	if !hasStepContaining(d.Steps, "dejima wsl setup") {
		t.Errorf("should offer the WSL2 local host; got %v", d.Steps)
	}
	if !hasStepContaining(d.Steps, "dejima profile add") {
		t.Errorf("should offer pointing at a server; got %v", d.Steps)
	}
	if !hasStepContaining(d.Steps, "dejima join") {
		t.Errorf("should offer joining via invite; got %v", d.Steps)
	}
	// Impossible-on-Windows remedies must not appear.
	for _, dead := range []string{"dejimad --foreground", "dejima service install", "systemctl", "launchd"} {
		if hasStepContaining(d.Steps, dead) {
			t.Errorf("step offers %q, which can't work on Windows; got %v", dead, d.Steps)
		}
	}
	// The default closing tells the user to run the fix "on the host shell" —
	// there is no host shell here, the commands run right where they are.
	if d.Closing == "" || strings.Contains(d.Closing, "host shell") {
		t.Errorf("closing should be Windows-appropriate, got %q", d.Closing)
	}
	if d.Remote {
		t.Error("this is the local-target diagnosis, not the remote one")
	}
}

// renderDaemonHelp must honour a diagnosis's own closing line, falling back to
// the local/remote defaults when it has none.
func TestRenderDaemonHelpClosing(t *testing.T) {
	custom := renderDaemonHelp(daemonDiagnosis{Cause: "c", Closing: "run it in PowerShell"})
	if !strings.Contains(custom, "run it in PowerShell") {
		t.Errorf("custom closing not rendered:\n%s", custom)
	}
	if strings.Contains(custom, "host shell") {
		t.Errorf("custom closing should replace the default, not add to it:\n%s", custom)
	}
	local := renderDaemonHelp(daemonDiagnosis{Cause: "c"})
	if !strings.Contains(local, "host shell") {
		t.Errorf("default local closing missing:\n%s", local)
	}
	remote := renderDaemonHelp(daemonDiagnosis{Cause: "c", Remote: true})
	if !strings.Contains(remote, "keeps retrying") {
		t.Errorf("default remote closing missing:\n%s", remote)
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
