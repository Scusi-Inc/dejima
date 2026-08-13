package main

import (
	"strings"
	"testing"
)

// On Windows, "run `dejima wsl setup`" is advice the user has to retype in
// PowerShell — and it's advice about the very machine the client is running on,
// so there is no host shell to go run it in. offerWSLSetup decides whether the
// daemon-help panel turns that into a keystroke instead.
//
// It exists as a pure function precisely so this can be tested from Linux. Both
// callers (renderDaemonHelp and handleKey's `w`) sit behind wsl.Supported(),
// which is false everywhere CI runs, so without the split neither branch would
// be exercised anywhere at all.
func TestOfferWSLSetup(t *testing.T) {
	cases := []struct {
		name   string
		d      daemonDiagnosis
		hasWSL bool
		want   bool
	}{
		{"windows, local target — the dead end this fixes", daemonDiagnosis{}, true, true},
		// A client pointed at someone else's server has no business being nudged
		// to build a local host; its diagnosis is about reaching that server.
		{"windows, remote target", daemonDiagnosis{Remote: true}, true, false},
		// Mac/Linux can host dejimad directly, so there is no WSL flow to offer
		// and the panel must not grow a key that does nothing.
		{"non-windows, local target", daemonDiagnosis{}, false, false},
		{"non-windows, remote target", daemonDiagnosis{Remote: true}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := offerWSLSetup(c.d, c.hasWSL); got != c.want {
				t.Errorf("offerWSLSetup(remote=%v, hasWSL=%v) = %v, want %v",
					c.d.Remote, c.hasWSL, got, c.want)
			}
		})
	}
}

// The half of the render that IS observable from here: a Mac/Linux operator
// staring at a down local daemon must not be told to press [w], because nothing
// is bound to it there. This is the regression the platform gate is for, and
// it's the direction that would actually reach users on this build.
func TestDaemonHelpHidesWSLOfferOffWindows(t *testing.T) {
	out := renderDaemonHelp(daemonDiagnosis{
		Cause: "dejimad isn't running",
		Steps: []string{"start it: dejima service install"},
	})
	if strings.Contains(out, "[w]") || strings.Contains(strings.ToLower(out), "wsl") {
		t.Errorf("off Windows the panel must not offer the WSL action:\n%s", out)
	}
	// Sanity: the panel still renders its actual content, so an empty-string bug
	// can't make the assertion above pass vacuously.
	if !strings.Contains(out, "dejimad isn't running") {
		t.Errorf("panel lost its cause line:\n%s", out)
	}
}
