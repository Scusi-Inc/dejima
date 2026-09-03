package wsl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The dial command must carry NOTHING the WSL channel can lose.
//
// An operator's client failed with
//
//	socat E connect(, AF=1 "<anon>", 2): Invalid argument
//
// AF=1 is AF_UNIX and length 2 is a sockaddr with an EMPTY path — socat was
// asked to connect to "". Not a missing socket (ENOENT would say so) but an
// argument that arrived blank, while the daemon was up and serving and the same
// socat command run by hand inside the distro returned an HTTP response.
//
// The dial script had a shell variable in it. This file's history records two
// other things this channel ate: a counter that produced `sh: 18: [: Illegal
// number:` because "the variable arrived empty with its quotes intact", and an
// unset HOME that produced `mkdir: cannot create directory ”`.
//
// startDaemonInWSL already drew the conclusion and wrote it down — "no shell
// variables anywhere. Paths are read once and interpolated in Go" — and start
// has been reliable since. The dial never got the same treatment, which is how
// `dejima wsl status` and `dejima wsl start` both worked on a machine where
// nothing could connect.
//
// So this asserts the ABSENCE of the whole class rather than that one variable
// resolves. A test that HOME expands correctly passes on a channel that mangles
// something else; a test that there is nothing to expand does not.
func TestTheDialCommandCarriesNothingExpandable(t *testing.T) {
	script := dialScript("/root/.dejima/dejimad.sock")
	for _, bad := range []struct{ frag, why string }{
		{"$", "a shell variable or command substitution"},
		{"`", "a backquoted command substitution"},
		{"\n", "a newline, which this channel has split before"},
		{"\"", "a double quote, which arrives inconsistently"},
		{"'", "a single quote, which arrives inconsistently"},
	} {
		if strings.Contains(script, bad.frag) {
			t.Errorf("the dial command contains %s (%q) — this channel has lost each "+
				"of these on a real machine, and the failure surfaces as socat "+
				"connecting to an empty path:\n%s", bad.why, bad.frag, script)
		}
	}
	if !strings.Contains(script, "/root/.dejima/dejimad.sock") {
		t.Errorf("the socket path is not in the command at all, so there is nothing "+
			"for the assertions above to be about:\n%s", script)
	}
}

// And the command must still work: connect to the socket it names, after waiting
// for a socket that shows up late.
//
// The control for the test above. "Contains no $" is satisfied by the empty
// string, so without this a dial that does nothing at all passes.
func TestTheDialCommandStillWaitsAndConnects(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh here")
	}
	dir := shortHome(t)
	sock := filepath.Join(dir, ".dejima", "dejimad.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	record := filepath.Join(bin, "target")
	write(t, filepath.Join(bin, "socat"),
		"#!/bin/sh\nprintf '%s' \"$2\" > "+record+"\nexit 0\n")

	cmd := exec.Command("/bin/sh", "-c", dialScript(sock))
	// A deliberately hostile environment: no HOME at all. The point of resolving
	// the path in Go is that the in-distro environment stops mattering.
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the dial command failed outright: %v\n%s", err, out)
	}
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("socat never ran, so nothing was dialled: %v", err)
	}
	got := strings.TrimPrefix(string(b), "UNIX-CONNECT:")
	if got == "" {
		t.Fatal("socat was asked to connect to an EMPTY path — this is the operator's " +
			"failure exactly: AF_UNIX, sockaddr length 2, \"Invalid argument\"")
	}
	if got != sock {
		t.Errorf("dialled %q, want %q", got, sock)
	}
}

// An unusable HOME must be reported as an unusable HOME, not turned into a path.
//
// The old code built "$HOME/.dejima/dejimad.sock" and handed the result to
// socat whatever it came out as, so an empty HOME surfaced as a socat error
// about an anonymous address — naming the tool, three layers from the cause.
func TestAnUnusableHomeIsNamedNotPapered(t *testing.T) {
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	for _, tc := range []struct{ name, out string }{
		{"empty", ""},
		{"not absolute", "root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketPathMu.Lock()
			delete(socketPathCache, "d")
			socketPathMu.Unlock()
			execCommand = func(_ string, _ ...string) *exec.Cmd {
				return exec.Command("/bin/sh", "-c", "printf '%s' "+shQuoteForTest(tc.out))
			}
			_, err := socketPathFor(t.Context(), "d")
			if err == nil {
				t.Fatal("an unusable HOME produced a socket path instead of an error")
			}
			if !strings.Contains(err.Error(), "HOME") {
				t.Errorf("the error does not name HOME, so it points at the wrong "+
					"layer: %v", err)
			}
		})
	}
}

func shQuoteForTest(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
