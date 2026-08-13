package main

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// exitErr builds an *exec.ExitError carrying code, the way a failed ssh does.
// `false` exits 1; for other codes we use sh so the test stays portable.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("expected a non-zero exit for code %d", code)
	}
	return err
}

// The operator-reported failure, verbatim. An unenrolled key must produce the
// enrollment remedy — and must NEVER produce `dejima upgrade`, which recreates
// the container and restarts every agent in the island to fix a problem that
// isn't in the container at all. That misrouting is the bug this guards.
func TestSSHFailureHintOnPublickeyDenial(t *testing.T) {
	const stderr = "wildfire@100.77.85.107: Permission denied (publickey).\n"
	hint := sshFailureHint(stderr, exitErr(t, 255))
	if hint == "" {
		t.Fatal("a publickey denial must be classified as an ssh-layer failure")
	}
	if !strings.Contains(hint, "dejima ssh enroll") {
		t.Errorf("hint should name the enrollment remedy, got: %q", hint)
	}
	if strings.Contains(hint, "dejima upgrade") {
		t.Errorf("hint must never recommend recreating the container, got: %q", hint)
	}
}

func TestSSHFailureHintClassifies(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string // a substring the remedy must contain
	}{
		{"publickey", "user@h: Permission denied (publickey).", "dejima ssh enroll"},
		{"too many auth failures", "Received disconnect: Too many authentication failures", "dejima ssh enroll"},
		{"host key changed", "@@@ REMOTE HOST IDENTIFICATION HAS CHANGED! @@@", "dejima doctor"},
		{"host key verification", "Host key verification failed.", "dejima doctor"},
		{"refused", "ssh: connect to host h port 2222: Connection refused", "dejima ssh info"},
		{"timeout", "ssh: connect to host h port 2222: Connection timed out", "tailnet"},
		{"no route", "ssh: connect to host h port 2222: No route to host", "tailnet"},
		{"dns", "ssh: Could not resolve hostname h: Name or service not known", "resolve"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sshFailureHint(c.output, exitErr(t, 255))
			if !strings.Contains(got, c.want) {
				t.Errorf("hint = %q, want it to mention %q", got, c.want)
			}
		})
	}
}

// The complement, and the reason this can't just check "did the probe fail":
// a command that RAN in the container and failed on its own terms is not an
// ssh-layer fault, so it must fall through to the real token diagnosis.
func TestSSHFailureHintIgnoresInContainerFailures(t *testing.T) {
	if got := sshFailureHint("", nil); got != "" {
		t.Errorf("no error should mean no hint, got %q", got)
	}
	// openclaw is missing / the key isn't set: ssh connected fine, the remote
	// command exited non-zero. Exit 1, not ssh's reserved 255.
	got := sshFailureHint("sh: 1: openclaw: not found\n", exitErr(t, 127))
	if got != "" {
		t.Errorf("a remote command failure is not an ssh fault, got hint %q", got)
	}
	if got := sshFailureHint("no such config key\n", exitErr(t, 1)); got != "" {
		t.Errorf("a remote command failure is not an ssh fault, got hint %q", got)
	}
}

// Unrecognised text with ssh's reserved 255 still has to be caught — otherwise
// an ssh failure we don't have a string for falls through to the token
// diagnosis and we're back to recommending a container restart.
func TestSSHFailureHintFallsBackTo255(t *testing.T) {
	got := sshFailureHint("kex_exchange_identification: read: some novel failure\n", exitErr(t, 255))
	if got == "" {
		t.Fatal("exit 255 is ssh's own failure code and must be classified")
	}
	if strings.Contains(got, "dejima upgrade") {
		t.Errorf("must not recommend recreating the container, got: %q", got)
	}
}

// freePort returns a port with nothing listening on it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func TestWaitForForwardReadyWhenListening(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if err := waitForForward(context.Background(), port, make(chan struct{}), 5*time.Second); err != nil {
		t.Fatalf("a listening forward should be ready, got %v", err)
	}
}

// The case that produced the bug: ssh authenticated-failed and died, so the
// forward never listened. waitForForward must report that rather than let the
// caller proceed to probe and open a browser.
func TestWaitForForwardDetectsExit(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	err := waitForForward(context.Background(), freePort(t), exited, 5*time.Second)
	if !errors.Is(err, errForwardExited) {
		t.Fatalf("err = %v, want errForwardExited", err)
	}
}

// A port that answers while ssh is dead belongs to someone else — the `--port`
// collision case, where ssh exits on "Address already in use" and an unrelated
// service is left holding the port. Reporting that as ready would open the
// browser onto a stranger's app, so exit must beat a successful dial.
func TestWaitForForwardExitBeatsAForeignListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	exited := make(chan struct{})
	close(exited)
	err = waitForForward(context.Background(), l.Addr().(*net.TCPAddr).Port, exited, time.Second)
	if !errors.Is(err, errForwardExited) {
		t.Fatalf("err = %v, want errForwardExited — a live port with a dead ssh is not our tunnel", err)
	}
}

// ssh alive but never binding (a hung handshake) must not block forever.
func TestWaitForForwardTimesOut(t *testing.T) {
	err := waitForForward(context.Background(), freePort(t), make(chan struct{}), 150*time.Millisecond)
	if !errors.Is(err, errForwardTimeout) {
		t.Fatalf("err = %v, want errForwardTimeout", err)
	}
}

// Ctrl-C during the wait surfaces as the context error, so the caller can return
// the process's own status instead of a spurious forward failure.
func TestWaitForForwardHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForForward(ctx, freePort(t), make(chan struct{}), 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
