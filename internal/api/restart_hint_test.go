package api

import (
	"runtime"
	"strings"
	"testing"
)

// The remedy printed after a failed self-update restart must apply to the host
// that printed it. It used to name `launchctl` unconditionally, so a daemon
// inside WSL — where `systemctl restart` had just failed with exit 5, because
// WSL ships no systemd — was told to run a macOS command.
//
// Every branch is driven directly. Reading the host instead meant the WSL case,
// the only one that was actually wrong in the field, ran only on a machine that
// happened to have no systemd — and a mutation breaking the systemd probe passed
// because the test skipped the case rather than failing it.
func TestRestartHintFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		goos    string
		systemd bool
		unit    bool
		want    string
		notWant []string
	}{
		{"macOS", "darwin", false, false, "launchctl", []string{"systemctl", "wsl"}},
		// --user, because that is the unit dejima installs and the command that
		// actually runs. Plain `systemctl restart dejimad` addresses a SYSTEM unit
		// that does not exist, so an operator who ran what the hint said got a
		// different failure than the one being reported to them.
		{"linux, systemd, unit installed", "linux", true, true, "systemctl --user restart dejimad.service", []string{"launchctl"}},
		{"linux without systemd (a container)", "linux", false, false, "dejima wsl start", []string{"launchctl"}},
		// THE FIELD CASE, AND THE ONE THIS MATRIX DID NOT HAVE. A modern WSL
		// distro RUNS systemd, so the old two-parameter version answered "yes" and
		// told the operator to run `sudo systemctl restart dejimad` — moments
		// after the daemon's own `systemctl --user restart` had failed with "is
		// the service installed?". systemd was never the question; whether there
		// is a service to restart into is.
		//
		// The old table drove every branch it had, which is why it passed: the
		// combination that occurs in the field was not one of them.
		{"linux, systemd RUNNING, no dejimad unit (WSL)", "linux", true, false, "dejima wsl start", []string{"launchctl", "sudo systemctl"}},
		{"anything else still says something", "windows", false, false, "Restart the daemon", []string{"launchctl", "systemctl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := restartHintFor(tc.goos, tc.systemd, tc.unit)
			if !strings.Contains(got, tc.want) {
				t.Errorf("restartHintFor(%q, systemd=%v, unit=%v) = %q, want it to contain %q",
					tc.goos, tc.systemd, tc.unit, got, tc.want)
			}
			for _, bad := range tc.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("restartHintFor(%q, systemd=%v, unit=%v) = %q, must not mention %q",
						tc.goos, tc.systemd, tc.unit, got, bad)
				}
			}
		})
	}
}

// The hint must not send an operator to a service that is not there.
//
// Stated separately from the table because it is the invariant, not a case: no
// combination may recommend systemctl when the unit is absent. A table can grow
// a row that violates it; this cannot pass while any does.
func TestNoSystemctlAdviceWithoutAUnit(t *testing.T) {
	for _, systemd := range []bool{true, false} {
		got := restartHintFor("linux", systemd, false)
		if strings.Contains(got, "systemctl") {
			t.Errorf("with no dejimad unit installed (systemd=%v) the hint still names "+
				"systemctl, which is the command that had just failed: %q", systemd, got)
		}
	}
}

// hasSystemd must test for a RUNNING systemd, not merely for systemctl on PATH:
// WSL images ship the binary with no init, which is exactly how the restart got
// attempted and failed. Asserted against this host's real state.
func TestHasSystemdMatchesTheRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only")
	}
	if got, want := hasSystemd(), dirExists("/run/systemd/system"); got != want {
		t.Errorf("hasSystemd() = %v, but /run/systemd/system present = %v — it is not reading the runtime", got, want)
	}
}

func dirExists(p string) bool {
	_, err := osStat(p)
	return err == nil
}
