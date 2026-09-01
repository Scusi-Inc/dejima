package wsl

import (
	"os/exec"
	"strings"
	"testing"
)

// Finding wsl.exe is not the same question as "is WSL installed".
//
// Windows ships wsl.exe in System32 whether or not the feature is enabled; on a
// box without it the launcher is a stub that prints "The Windows Subsystem for
// Linux is not installed" and exits non-zero. Available() used to report true on
// the strength of LookPath alone, so requireWSLPlatform's clear instruction never
// fired and the operator got a raw passthrough from three layers down:
//
//	wsl -l -v: The Windows Subsystem for Linux is not installed.
//
// The guard was not missing. It was not reached.
func TestReportsNotInstalledSeparatesAbsentFromEmpty(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{{
		name: "the stub launcher on a box with no WSL",
		text: "The Windows Subsystem for Linux is not installed.\r\nUse 'wsl --install' to install it.",
		want: true,
	}, {
		name: "lowercase variant",
		text: "wsl is not installed",
		want: true,
	}, {
		// The case that must NOT be read as absence. `wsl --status` also exits
		// non-zero here, so keying off the exit code would send someone with a
		// working WSL to reinstall Windows features — the opposite of the fix
		// they need, which is `dejima wsl setup`.
		name: "installed, but no distro yet",
		text: "Default Version: 2\r\nWSL version: 2.0.9.0",
		want: false,
	}, {
		name: "a healthy install",
		text: "Default Distribution: dejima\r\nDefault Version: 2",
		want: false,
	}, {
		// A localized Windows translates the message, so it reads as installed
		// and the operator gets today's raw passthrough rather than a WRONG
		// instruction. That is the safe direction to be wrong in, and it is a
		// deliberate choice rather than an oversight.
		name: "a translated message is not recognised, and that is intended",
		text: "Le sous-système Windows pour Linux n'est pas installé.",
		want: false,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reportsNotInstalled(tc.text); got != tc.want {
				t.Errorf("reportsNotInstalled(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// featurePresent must actually run the probe and read its output.
//
// The first version of this file tested only reportsNotInstalled, so reverting
// the probe to `return true` — the exact bug — changed nothing. A helper proved
// correct beside a caller that never consults it is the shape being fixed here,
// one level up.
func TestFeaturePresentReadsTheProbeOutput(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })

	stub := func(output string, fail bool) {
		execCommand = func(_ string, _ ...string) *exec.Cmd {
			// `printf` then optionally exit non-zero, so the test drives BOTH the
			// text and the exit status independently — the whole point is that the
			// text decides and the status does not.
			script := "printf '%s' " + shellQuote(output)
			if fail {
				script += "; exit 1"
			}
			return exec.Command("sh", "-c", script)
		}
	}

	stub("The Windows Subsystem for Linux is not installed.", true)
	if featurePresent() {
		t.Error("reported WSL present against the stub launcher's own answer — " +
			"requireWSLPlatform's instruction never fires and the operator gets a " +
			"raw passthrough from three layers down")
	}

	// Installed with no distro: `--status` exits NON-ZERO here too. Keying off the
	// exit code would send someone with a working WSL to reinstall Windows
	// features instead of running `dejima wsl setup`.
	stub("Default Version: 2", true)
	if !featurePresent() {
		t.Error("a non-zero exit was read as absence — that is the no-distro state, " +
			"which needs `dejima wsl setup`, not a Windows feature install")
	}

	stub("Default Distribution: dejima\nDefault Version: 2", false)
	if !featurePresent() {
		t.Error("a healthy install was reported absent")
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Three user-facing sites print the install command and a fourth (the website)
// copies it. They HAD drifted — the binary said `wsl --install`, the site said
// `--no-distribution` — so the same operator got two different instructions
// depending on where they read. One constant, so the next drift is impossible.
func TestInstallHintNamesBothForms(t *testing.T) {
	if !strings.Contains(InstallHint, "--no-distribution") {
		t.Error("the hint omits --no-distribution, so it tells the operator to download " +
			"a default Ubuntu that `dejima wsl setup` never uses — a distro, a reboot-time " +
			"username prompt, and the belief that what they set up is what we run")
	}
	// The flag needs a recent wsl.exe. Naming only the modern form dead-ends an
	// older box on an unrecognised flag, with nothing to try next.
	if !strings.Contains(InstallHint, "older") {
		t.Error("the hint gives no fallback for an older wsl.exe that rejects the flag")
	}
	// The fallback path needs the update step: plain --install on an old wsl.exe
	// leaves it old. d4 walked this on a real box. It lives HERE rather than only
	// on the website because the site asserts its command block is a substring of
	// this constant — a step that exists only on the page is invisible to that
	// check and drifts silently, which is what this constant exists to prevent.
	if !strings.Contains(InstallHint, "wsl --update") {
		t.Error("the older-wsl.exe fallback stops before `wsl --update`, so the page " +
			"carries a step the binary does not and the substring check cannot see it")
	}
}
