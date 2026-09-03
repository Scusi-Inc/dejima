package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/wsl"
)

// The exact error the operator's client reported, so the routing guard below is
// driven by a real failure rather than a phrase invented to match the matcher.
const wslUnreachableErr = `daemon unreachable: Get "http://dejimad/v1/overview": wsl dial failed`

// remoteOnlyAdvice is language that is only ever true of a REMOTE daemon. Every
// entry appeared on the operator's screen for a distro on their own machine.
var remoteOnlyAdvice = []string{
	"tailscale",        // there is no tailnet in a wsl:// dial
	"install-client",   // reinstalling the Windows client cannot start a distro
	"ask the operator", // they ARE the operator
	"the server may be down",
	"on the server",
}

// A wsl:// target must never be handed remote-daemon advice.
//
// This is what the operator saw. Their profile pointed at wsl://dejima — a local
// socket tunnel through wsl.exe, with no TCP listener and no tailnet anywhere in
// it — and the dashboard told them to run `tailscale status`, `tailscale ping
// wsl`, reinstall the Windows client, and failing all that to "ask the operator,
// or check the host". They are the operator. The host is the laptop they are
// holding. Not one of those steps can affect a WSL distro, and the one command
// that would have (`dejima wsl start`) was on neither list.
//
// The check is on the RENDERED panel rather than on which function was called,
// because what went wrong was what a person read.
func TestAWSLTargetIsNeverGivenRemoteAdvice(t *testing.T) {
	m := tuiModel{activeHost: "wsl://dejima"}
	out, _ := m.Update(errMsg{errors.New(wslUnreachableErr)})
	got, ok := out.(tuiModel)
	if !ok {
		t.Fatalf("Update returned %T", out)
	}
	if got.daemonHelp == nil {
		t.Fatal("a connection error on a wsl:// host produced no diagnosis at all, " +
			"so the operator gets the bare error with nothing to do about it")
	}
	// The STEPS, not the whole panel. The panel legitimately says "Tailscale and
	// TCP are not involved in a wsl:// connection" — explaining why the usual
	// network advice is absent is the opposite of giving it, and a substring
	// match over the rendered panel cannot tell those two apart. It caught this
	// test doing exactly that: it passed against correct code for the wrong
	// reason until the pre-probe wording changed underneath it.
	steps := strings.ToLower(strings.Join(got.daemonHelp.Steps, "\n"))
	for _, phrase := range remoteOnlyAdvice {
		if strings.Contains(steps, phrase) {
			t.Errorf("the offline panel for a LOCAL wsl:// distro tells the operator to %q — "+
				"none of the remote-daemon remedies can affect a WSL distro:\n%s", phrase, steps)
		}
	}
	// And the cause must name the transport, so the operator knows why the usual
	// network advice is absent rather than thinking it was forgotten.
	if !strings.Contains(strings.ToLower(got.daemonHelp.Cause), "wsl") {
		t.Errorf("the panel never mentions WSL, so it does not explain itself:\n%s",
			got.daemonHelp.Cause)
	}
}

// A non-WSL remote host must still get the remote advice.
//
// The control. Without it the guard above passes just as well on a change that
// breaks the remote path — and the remote path is the one most users are on.
func TestARemoteTargetStillGetsRemoteAdvice(t *testing.T) {
	m := tuiModel{activeHost: "mac-mini:7273"}
	out, _ := m.Update(errMsg{errors.New(wslUnreachableErr)})
	got := out.(tuiModel)
	if got.daemonHelp == nil {
		t.Fatal("a remote host lost its diagnosis")
	}
	steps := strings.ToLower(strings.Join(got.daemonHelp.Steps, "\n"))
	if !strings.Contains(steps, "tailscale status") {
		t.Errorf("a genuinely remote host no longer gets the tailnet check:\n%s", steps)
	}
}

// The `w` → `dejima wsl setup` shortcut belongs to a WSL target.
//
// offerWSLSetup gates on !Remote, and routing wsl:// through the remote path set
// Remote true — so the one operator the keystroke was built for was the one
// operator who could not see it. On a machine with WSL, being unable to reach a
// WSL distro is precisely when setup is the answer.
func TestTheWSLSetupShortcutIsOfferedForAWSLTarget(t *testing.T) {
	d := diagnoseWSLDaemon("dejima", nil, errWSLProbePending)
	if !offerWSLSetup(d, true) {
		t.Error("a WSL-target diagnosis does not offer `dejima wsl setup`, which is the " +
			"remedy for most of its causes and is one keystroke away")
	}
	if offerWSLSetup(diagnoseRemoteDaemon("mac-mini:7273"), true) {
		t.Error("a genuinely remote server should not offer local WSL setup")
	}
}

// Each probe state names the remedy that fits it, and only that one.
func TestTheWSLDiagnosisNamesTheRemedyForEachState(t *testing.T) {
	ready := wsl.Report{Distro: "dejima", Exists: true, Version: 2,
		HasSocat: true, HasDocker: true, HasDejima: true, SocketUp: true}
	for _, tc := range []struct {
		name string
		rep  wsl.Report
		want string // the command that must appear
		deny string // a command that must NOT, because it cannot help
	}{
		{
			name: "no distro at all",
			rep:  wsl.Report{Distro: "dejima"},
			want: "dejima wsl setup",
			deny: "dejima wsl start", // starting a distro that does not exist
		},
		{
			// The reported case: setup ran, socat did not land, and the daemon may
			// be running perfectly. Nothing about the daemon is wrong.
			name: "socat missing — the tunnel itself",
			rep:  wsl.Report{Distro: "dejima", Exists: true, Version: 2, HasDocker: true, HasDejima: true},
			want: "dejima wsl setup",
			deny: "dejima wsl start",
		},
		{
			// Everything installed, daemon down. The common state after a reboot,
			// and the one the tailnet advice buried deepest.
			name: "installed but not running",
			rep: wsl.Report{Distro: "dejima", Exists: true, Version: 2,
				HasSocat: true, HasDocker: true, HasDejima: true},
			want: "dejima wsl start",
			deny: "dejima wsl setup", // re-running setup will not start a daemon
		},
		{
			name: "wsl1 cannot be repaired by setup alone",
			rep:  wsl.Report{Distro: "dejima", Exists: true, Version: 1},
			want: "wsl --set-version dejima 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := tc.rep
			d := diagnoseWSLDaemon("dejima", &rep, nil)
			text := d.Cause + "\n" + strings.Join(d.Steps, "\n")
			if !strings.Contains(text, tc.want) {
				t.Errorf("does not name %q:\n%s", tc.want, text)
			}
			if tc.deny != "" && strings.Contains(text, tc.deny) {
				t.Errorf("offers %q, which cannot fix this state:\n%s", tc.deny, text)
			}
		})
	}

	// A healthy-looking distro must not invent a cause it cannot see.
	d := diagnoseWSLDaemon("dejima", &ready, nil)
	if !strings.Contains(d.Cause, "cannot see") {
		t.Errorf("with every check passing and the dial still failing, the diagnosis "+
			"must say it does not know rather than pick a plausible cause:\n%s", d.Cause)
	}
}

// An un-probed distro must not be rendered as a finding.
//
// The TUI paints before the probe returns, because a probe boots a stopped
// distro and takes seconds. "I have not looked yet" and "I looked and found
// nothing wrong" are different facts, and rendering the first as the second is
// the same mistake CredentialMountReport.Known exists to prevent.
func TestAnUnprobedDistroSaysSoRatherThanGuessing(t *testing.T) {
	d := diagnoseWSLDaemon("dejima", nil, errWSLProbePending)
	text := strings.ToLower(d.Cause + "\n" + strings.Join(d.Steps, "\n"))
	if !strings.Contains(text, "checking") {
		t.Errorf("the pre-probe panel does not say it is still looking:\n%s", text)
	}
	for _, claim := range []string{"does not exist", "socat is missing", "is not installed"} {
		if strings.Contains(text, claim) {
			t.Errorf("the pre-probe panel claims %q without having looked:\n%s", claim, text)
		}
	}
}
