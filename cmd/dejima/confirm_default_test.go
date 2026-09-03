package main

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

// withPromptIO points the shared prompt reader and writer at test buffers,
// returning what the prompt printed. Both are package-level vars, so they must
// be restored or the next test reads this one's leftovers.
func withPromptIO(t *testing.T, typed string, fn func()) string {
	t.Helper()
	oldIn, oldOut := stdinReader, promptOut
	var out bytes.Buffer
	stdinReader = bufio.NewReader(strings.NewReader(typed))
	promptOut = io.Writer(&out)
	t.Cleanup(func() { stdinReader, promptOut = oldIn, oldOut })
	fn()
	return out.String()
}

// A confirm prompt's DISPLAYED default must be the one it RETURNS.
//
// Nothing asserted this before. The two live in separate statements — a string
// with `[Y/n]` in it, and a branch deciding what an empty answer means — and
// they can drift apart in either direction. "[y/N] that actually defaults to
// yes" is the dangerous one: someone presses Enter expecting to decline and the
// thing happens anyway. "[Y/n] that actually defaults to no" is the one that
// shipped: `dejima wsl setup` cancelled on Enter at both of its questions.
//
// Both read fine in review, because each half is individually correct.
//
// The expectation here is derived from the PROMPT TEXT, not from the argument.
// Asserting `confirmDefault(q, true) == true` would only restate the call; it
// would pass just as happily with the bracket printed backwards, which is the
// entire failure being guarded.
func TestConfirmDefaultReturnsWhatItDisplays(t *testing.T) {
	for _, defYes := range []bool{true, false} {
		var got bool
		prompt := withPromptIO(t, "\n", func() {
			got = confirmDefault("Do the thing?", defYes)
		})

		var displayed bool
		switch {
		case strings.Contains(prompt, "[Y/n]"):
			displayed = true
		case strings.Contains(prompt, "[y/N]"):
			displayed = false
		default:
			// A guard that cannot find its subject must fail, not pass quietly.
			t.Fatalf("prompt %q shows no [Y/n] or [y/N] at all, so there is no "+
				"displayed default to compare against — this guard is checking "+
				"nothing", prompt)
		}

		if got != displayed {
			t.Errorf("prompt %q displays default=%v but Enter returned %v — the "+
				"operator is told one thing and gets the other", prompt, displayed, got)
		}
	}
}

// Typing an answer must still beat the default, in both directions. Without
// this, "always return defYes" satisfies the agreement check above.
func TestConfirmDefaultHonorsATypedAnswer(t *testing.T) {
	for _, tc := range []struct {
		typed  string
		defYes bool
		want   bool
	}{
		{"n\n", true, false},
		{"no\n", true, false},
		{"y\n", false, true},
		{"yes\n", false, true},
		{"Y\n", false, true},
	} {
		var got bool
		withPromptIO(t, tc.typed, func() {
			got = confirmDefault("Do the thing?", tc.defYes)
		})
		if got != tc.want {
			t.Errorf("typed %q with default=%v: got %v, want %v — a typed answer "+
				"must override the default", strings.TrimSpace(tc.typed), tc.defYes, got, tc.want)
		}
	}
}

// The specific regression: Enter at either `dejima wsl setup` question proceeds.
//
// The operator reported this as a capitalisation typo. It was not — the bracket
// was accurate. Enter really did cancel the distro creation and the Docker
// install, on a command whose whole purpose is to do those two things, while
// the installer that launched it answers its own questions with Enter=yes.
func TestConfirmWSLProceedsOnEnter(t *testing.T) {
	prompt := withPromptIO(t, "\n", func() {
		if !confirmWSL("Create it now?") {
			t.Error("Enter cancelled distro creation — `dejima wsl setup` exists " +
				"to create the distro, and --yes already covers unattended runs")
		}
	})
	if !strings.Contains(prompt, "[Y/n]") {
		t.Errorf("prompt %q does not advertise a yes default", prompt)
	}
	withPromptIO(t, "n\n", func() {
		if confirmWSL("Create it now?") {
			t.Error("typing n no longer cancels")
		}
	})
}

// Silence is not consent. A closed or absent stdin must answer NO even where the
// displayed default is yes.
//
// This is the property the old confirmWSL comment claimed — "a non-TTY answers
// no, so a piped invocation can't silently install things" — and it was true only
// because that prompt defaulted to no. Flipping the default without this would
// have made `dejima wsl setup`, run from a script or a service with no terminal,
// create a distro and install Docker with nobody having agreed to either. --yes
// is how a script says yes.
func TestConfirmDefaultTreatsNoStdinAsNo(t *testing.T) {
	for _, defYes := range []bool{true, false} {
		var got bool
		withPromptIO(t, "", func() { // EOF immediately: no terminal, no answer
			got = confirmDefault("Install a thing?", defYes)
		})
		if got {
			t.Errorf("default=%v: EOF on stdin was read as consent — an unattended "+
				"run would install something nobody agreed to", defYes)
		}
	}
}

// ...but a real answer arriving without a trailing newline still counts. This is
// the same EOF that means "nobody there" when the buffer is empty, so the two
// cases are told apart by whether anything was typed, not by the error alone.
func TestConfirmDefaultAcceptsAnswerWithoutTrailingNewline(t *testing.T) {
	var got bool
	withPromptIO(t, "y", func() { got = confirmDefault("Do it?", false) })
	if !got {
		t.Error("a typed y with no trailing newline was discarded as EOF")
	}
}
