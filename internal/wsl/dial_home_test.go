package wsl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The dial must resolve $HOME itself, because nothing else will.
//
// `wsl.exe -d <distro> -- sh -c …` does not pass HOME, and /bin/sh on Ubuntu is
// dash, which does not synthesise it from the passwd entry. Dial ran socketExpr
// bare, so $HOME was empty and the client looked for the daemon at
// /.dejima/dejimad.sock — a path nothing creates. It waited its five seconds and
// reported the host as not answering.
//
// What made it survive is that the check and the dial disagreed and only one was
// tested. `dejima wsl status` goes through run(), which DOES prepend
// homePreamble, so an operator saw:
//
//	socket:  OK    up (~/.dejima/dejimad.sock)
//	ready — connect with:  dejima profile switch wsl
//
// with the daemon's own log confirming it was listening on
// /root/.dejima/dejimad.sock, while every dial from that same client failed.
//
// AND THE EXISTING COLD-BOOT TEST COULD NOT CATCH IT, because its fixture sets
// HOME in the environment it runs socketExpr under. It supplied the exact thing
// whose absence is the bug. So this one runs with HOME UNSET — the shape
// wsl.exe actually hands the script.
func TestDialResolvesHomeWithoutOneInTheEnvironment(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil || realHome == "" || realHome == "/" {
		t.Skipf("this user has no usable passwd home to resolve to (%q, %v)", realHome, err)
	}
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		t.Skipf("no %s here", sh)
	}

	// A stub socat that records the path it was asked to connect to. The bug is
	// entirely about which path that is.
	bin := t.TempDir()
	record := filepath.Join(bin, "target")
	write(t, filepath.Join(bin, "socat"),
		"#!/bin/sh\nprintf '%s' \"$2\" > "+record+"\nexit 0\n")

	cmd := exec.Command(sh, "-c", dialExpr)
	// NO HOME. This is the whole point: wsl.exe does not pass one.
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the dial script failed outright: %v\n%s", err, out)
	}

	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("socat never ran, so nothing was dialled at all: %v", err)
	}
	got := strings.TrimPrefix(string(b), "UNIX-CONNECT:")
	if got == "/.dejima/dejimad.sock" {
		t.Fatalf("the dial resolved $HOME to the empty string and looked for the "+
			"daemon at %s — the exact path an operator's client sat waiting on "+
			"while `dejima wsl status` reported the socket up", got)
	}
	if want := filepath.Join(realHome, ".dejima", "dejimad.sock"); got != want {
		t.Errorf("dialled %q, want %q", got, want)
	}
}

// The status check and the dial must resolve HOME the same way.
//
// They did not, and that disagreement is why every other signal said fine. This
// asserts the two share one derivation rather than each carrying its own — a
// second copy would pass this test on the day it was written and drift after.
func TestTheDialAndTheStatusCheckShareOneHomeDerivation(t *testing.T) {
	if !strings.Contains(dialExpr, homePreamble) {
		t.Error("the dial does not use homePreamble, so it resolves HOME by some " +
			"other means than run() — which is exactly the split that let `dejima " +
			"wsl status` report ready while every dial failed")
	}
	if !strings.Contains(dialExpr, socketExpr) {
		t.Error("the dial no longer contains socketExpr, so the cold-boot wait that " +
			"file tests is not on the path Dial actually runs")
	}
}
