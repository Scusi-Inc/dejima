package main

import (
	"strings"
	"testing"
)

// The steps are the whole point: every façade-off surface must name BOTH the
// host enable command and the client enroll step. A guidance message missing
// either half is what sent the operator hand-reassembling flags.
func TestSSHFacadeSetupStepsNameBothSteps(t *testing.T) {
	steps := sshFacadeSetupSteps()
	for _, want := range []string{
		"service install --system", // step 1: enable on the host
		"--ssh",                    // …with the façade flag
		"ssh enroll",               // step 2: enroll this device
		"sudo",                     // step 1 needs root
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("setup steps missing %q:\n%s", want, steps)
		}
	}
}

// The TUI form points the client step at the in-TUI enroll (m → SSH setup),
// not the CLI, since the pane already has that action.
func TestSSHFacadeSetupStepsTUIPointsAtInTUIEnroll(t *testing.T) {
	steps := sshFacadeSetupStepsTUI()
	if !strings.Contains(steps, "service install") {
		t.Errorf("TUI steps should still name the host command:\n%s", steps)
	}
	if !strings.Contains(steps, "SSH setup") {
		t.Errorf("TUI steps should point at the in-TUI enroll (m → SSH setup):\n%s", steps)
	}
}

// stripFlag removes a stale --ssh (space- or =-joined) so a reconstructed
// command doesn't carry two, whichever form the plist used.
func TestStripFlag(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"space-joined", []string{"--tcp", ":7273", "--ssh", "1.2.3.4:2222", "--audit"}, []string{"--tcp", ":7273", "--audit"}},
		{"equals-joined", []string{"--tcp", ":7273", "--ssh=1.2.3.4:2222"}, []string{"--tcp", ":7273"}},
		{"absent", []string{"--tcp", ":7273", "--audit"}, []string{"--tcp", ":7273", "--audit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripFlag(tc.in, "--ssh")
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("stripFlag = %v, want %v", got, tc.want)
			}
		})
	}
}

// The suggested address always carries the :2222 port so the operator can paste
// it verbatim, whether or not Tailscale resolved a real IP.
func TestSuggestedSSHAddrHasPort(t *testing.T) {
	if !strings.HasSuffix(suggestedSSHAddr(), ":2222") {
		t.Errorf("suggested addr should end in :2222, got %q", suggestedSSHAddr())
	}
}
