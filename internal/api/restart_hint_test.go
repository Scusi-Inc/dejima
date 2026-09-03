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
		want    string
		notWant []string
	}{
		{"macOS", "darwin", false, "launchctl", []string{"systemctl", "wsl"}},
		{"linux with systemd", "linux", true, "systemctl restart dejimad", []string{"launchctl"}},
		// THE FIELD CASE: systemctl restart just failed with exit 5 here, so
		// repeating it is a circle, and launchctl does not exist.
		{"linux without systemd (WSL)", "linux", false, "dejima wsl start", []string{"launchctl", "systemctl"}},
		{"anything else still says something", "windows", false, "Restart the daemon", []string{"launchctl", "systemctl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := restartHintFor(tc.goos, tc.systemd)
			if !strings.Contains(got, tc.want) {
				t.Errorf("restartHintFor(%q, %v) = %q, want it to contain %q", tc.goos, tc.systemd, got, tc.want)
			}
			for _, bad := range tc.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("restartHintFor(%q, %v) = %q, must not mention %q", tc.goos, tc.systemd, got, bad)
				}
			}
		})
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
