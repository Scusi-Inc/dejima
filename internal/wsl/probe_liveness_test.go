package wsl

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestHelperUnixConnect stands in for socat's ONLY contract in probeScript:
// exit 0 if a unix connect succeeds, non-zero if it is refused.
//
// A stub is used because socat is not installed on this box or on CI, and the
// alternative — skipping — would mean the check that distinguishes a live
// daemon from a dead one is verified nowhere. The connect itself is real; only
// the binary that performs it is substituted.
func TestHelperUnixConnect(t *testing.T) {
	target := os.Getenv("DEJIMA_CONNECT_TARGET")
	if target == "" {
		return // not the helper invocation
	}
	c, err := net.Dial("unix", target)
	if err != nil {
		os.Exit(1)
	}
	_ = c.Close()
	os.Exit(0)
}

// stubSocat writes a `socat` onto PATH that connects for real via the helper.
func stubSocat(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary, so the stub cannot connect: %v", err)
	}
	bin := t.TempDir()
	write(t, filepath.Join(bin, "socat"), "#!/bin/sh\n"+
		"for a in \"$@\"; do case \"$a\" in UNIX-CONNECT:*) target=\"${a#UNIX-CONNECT:}\";; esac; done\n"+
		"DEJIMA_CONNECT_TARGET=\"$target\" exec "+self+" -test.run=TestHelperUnixConnect\n")
	return bin
}

// staleSocket leaves a socket FILE that nothing is listening on — what a killed
// daemon leaves behind.
//
// bind() creates the file; closing the fd without listening leaves it in place
// with nothing accepting, so a connect is refused. net.Listen is deliberately
// NOT used: it unlinks on Close, which is the one behaviour that must not happen
// here — the file surviving its process is the whole subject.
func staleSocket(t *testing.T, path string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		_ = syscall.Close(fd)
		t.Skipf("cannot bind %s: %v", path, err)
	}
	_ = syscall.Close(fd)
	st, err := os.Stat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the fixture left no socket file behind, so this is not the state "+
			"under test: %v", err)
	}
}

// runProbe executes the real probeScript under /bin/sh and returns its tokens.
func runProbe(t *testing.T, home, bin string) map[string]bool {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", probeScript)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the probe script failed outright: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, tok := range strings.Fields(string(out)) {
		got[tok] = true
	}
	return got
}

// A socket file left behind by a dead daemon must not report as live.
//
// Probe tested the socket with `[ -S ]` — "is there a socket file", which is a
// different question from "is anything accepting on it". The two diverge exactly
// when a daemon dies without unlinking, which is every time WSL terminates an
// idle distro.
//
// Not hypothetical. An operator's `dejima wsl status` printed
//
//	socat:   OK    installed
//	docker:  OK    engine responding
//	dejimad: OK    installed
//	socket:  OK    up (~/.dejima/dejimad.sock)
//	ready — connect with:  dejima profile switch wsl
//
// while every dial failed, and dejimad's own log showed no start for thirty
// hours. Four checks agreed and the one that mattered was never made.
func TestAStaleSocketIsNotReportedAsLive(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh here")
	}
	home := shortHome(t)
	sock := filepath.Join(home, ".dejima", "dejimad.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	staleSocket(t, sock)

	got := runProbe(t, home, stubSocat(t))
	if !got["socket"] {
		t.Fatal("the fixture's socket file was not even seen, so the liveness " +
			"assertion below has no subject")
	}
	if got["listening"] {
		t.Error("a socket file with nothing accepting on it reported as listening — " +
			"this is the state that told an operator their distro was ready while " +
			"every dial was refused")
	}

	rep := Report{Exists: true, Version: 2, HasSocat: true, HasDocker: true,
		HasDejima: true, SocketUp: got["socket"], Listening: got["listening"]}
	if rep.Ready() {
		t.Error("Ready() is true for a daemon that is not accepting connections; " +
			"`dejima wsl status` prints \"ready — connect with: …\" off exactly this")
	}
}

// And a LIVE socket must still report live. The control: without it, a probe
// that never emits `listening` passes the test above and breaks every working
// distro.
func TestALiveSocketIsReportedAsLive(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh here")
	}
	home := shortHome(t)
	sock := filepath.Join(home, ".dejima", "dejimad.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", sock, err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	got := runProbe(t, home, stubSocat(t))
	if !got["socket"] || !got["listening"] {
		t.Fatalf("a daemon that IS accepting was not reported live (socket=%v "+
			"listening=%v) — this would tell every working distro it is not ready",
			got["socket"], got["listening"])
	}
	rep := Report{Exists: true, Version: 2, HasSocat: true, HasDocker: true,
		HasDejima: true, SocketUp: got["socket"], Listening: got["listening"]}
	if !rep.Ready() {
		t.Error("a distro with everything installed and a daemon accepting is not Ready()")
	}
}

// No socket file at all is a third state, not the stale one. Same remedy, but
// "never started" and "died without cleaning up" are different facts and the
// status text distinguishes them.
func TestNoSocketFileIsNeitherLiveNorStale(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh here")
	}
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".dejima"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := runProbe(t, home, stubSocat(t))
	if got["socket"] || got["listening"] {
		t.Errorf("an absent socket reported as socket=%v listening=%v",
			got["socket"], got["listening"])
	}
}
