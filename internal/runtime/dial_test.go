package runtime

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// DialContainerPort rests on a premise: that `bash -c 'exec 3<>/dev/tcp/h/p; …'`
// pumped over a subprocess's stdio behaves as a real bidirectional stream — good
// enough to carry HTTP and to survive a protocol upgrade.
//
// The gateway proxy's own tests substitute the dial entirely, so they exercise
// the proxy and say nothing about this. The chain was: documented bash behaviour
// → a fake that models it → a passing test. Every link reasonable and not one of
// them a measurement.
//
// These run the REAL script against a REAL listener. Docker is the only part
// stubbed out, because a container is not needed to find out whether the shell
// bridging works — and that separation is the point: the untestable half stays
// small and named.

// stubDockerExec writes a fake `docker` that drops the `exec -i <container>`
// prefix and runs the rest for real. What remains IS the production command:
// bash, -c, the dial script, argv.
func stubDockerExec(t *testing.T) *Docker {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	// "$1"=exec "$2"=-i "$3"=<container>; everything after is the real command.
	script := "#!/bin/sh\nshift 3\nexec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Docker{Bin: bin}
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable; the dial script needs it for /dev/tcp")
	}
}

// echoServer answers one request line by line, so a test can drive a
// conversation rather than a single exchange.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := io.WriteString(c, "echo:"+line); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln
}

func portOf(t *testing.T, ln net.Listener) int {
	t.Helper()
	return ln.Addr().(*net.TCPAddr).Port
}

// The premise, measured: bytes go both ways, more than once, over the real
// script.
func TestDialContainerPortCarriesAConversation(t *testing.T) {
	requireBash(t)
	ln := echoServer(t)
	d := stubDockerExec(t)

	conn, err := d.DialContainerPort(context.Background(), "isl", "127.0.0.1", portOf(t, ln))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	br := bufio.NewReader(conn)
	for _, word := range []string{"one", "two", "three"} {
		if _, err := io.WriteString(conn, word+"\n"); err != nil {
			t.Fatalf("write %q: %v", word, err)
		}
		got, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read after %q: %v", word, err)
		}
		if strings.TrimSpace(got) != "echo:"+word {
			t.Fatalf("got %q, want echo:%s", strings.TrimSpace(got), word)
		}
	}
}

// A dial to a port nothing is listening on must fail as a CONNECTION, not hang
// and not look successful. The script exits 1 without opening fd 3, so the
// caller sees EOF rather than a live stream.
func TestDialContainerPortToADeadPortEndsPromptly(t *testing.T) {
	requireBash(t)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(t, free)
	_ = free.Close() // now certainly closed

	d := stubDockerExec(t)
	conn, err := d.DialContainerPort(context.Background(), "isl", "127.0.0.1", port)
	if err != nil {
		return // an error here is a fine outcome too
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(conn, "hello\n"); err != nil {
		return
	}
	buf := make([]byte, 16)
	if n, err := conn.Read(buf); err == nil && n > 0 {
		t.Errorf("read %q from a port nothing is listening on — a dead dial must not "+
			"present as a live connection", buf[:n])
	}
}

// The proxy carries websockets, so the transport under it has to survive a
// protocol upgrade: an HTTP exchange followed by raw bytes on the same
// connection, in both directions.
func TestDialContainerPortSurvivesAnUpgrade(t *testing.T) {
	requireBash(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		for { // drain the request headers
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimSpace(line) == "" {
				break
			}
		}
		_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
		buf := make([]byte, 32)
		n, err := br.Read(buf)
		if err == nil && n > 0 {
			_, _ = c.Write(append([]byte("after:"), buf[:n]...))
		}
	}()

	d := stubDockerExec(t)
	conn, err := d.DialContainerPort(context.Background(), "isl", "127.0.0.1", portOf(t, ln))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	if _, err := io.WriteString(conn,
		"GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status = %q err = %v, want 101", strings.TrimSpace(status), err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	// ReadFull against a known length: this is a byte stream and a single Read
	// may return a partial. A test that assumed otherwise passed locally and
	// failed on CI once already this week.
	want := "after:ping"
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("the stream did not survive the upgrade: %v", err)
	}
	if string(buf) != want {
		t.Errorf("got %q, want %q", buf, want)
	}
}

// A closed conn must leave no `docker exec` behind. A console opens and drops
// many connections; one leak per drop piles up — and in an island whose PID 1 is
// `tail -f /dev/null`, piles up permanently.
//
// This asserts the OUTCOME, not which line produces it. Removing the reap from
// Close survives, because killing the process ends the stdout pump and the pump
// reaps on its way out. That redundancy is real and the test does not pretend
// otherwise: it says the subprocess is gone, which is the property that matters.
func TestDialContainerPortCloseReapsTheSubprocess(t *testing.T) {
	requireBash(t)
	ln := echoServer(t)
	d := stubDockerExec(t)

	conn, err := d.DialContainerPort(context.Background(), "isl", "127.0.0.1", portOf(t, ln))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ec, ok := conn.(*execConn)
	if !ok {
		t.Fatalf("dial returned %T, not *execConn — this test is not watching what it thinks", conn)
	}
	if err := ec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-ec.Reaped():
	case <-time.After(20 * time.Second):
		t.Fatal("the subprocess was not reaped after Close")
	}
	// Closing twice must be safe: http.Transport does it on some paths.
	if err := ec.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
}

// The control on all of the above. The stub has to be running the PRODUCTION
// command; if it swallowed or rewrote the argv, every test here would pass
// against something other than the code under test.
//
// Asserted on the recorded argv rather than by running a command of the test's
// own choosing — the first version of this control did the latter, through
// Exec(), whose argv prefix is `exec <name>` rather than the dial's
// `exec -i <name>`. It failed with exit 127, which was the right outcome for the
// wrong reason: it proved the two paths differ, not that the stub works.
func TestStubDockerExecRunsTheRealDialCommand(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "docker")
	// Record everything, then behave as the pass-through stub does.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + rec + "\nshift 3\nexec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ln := echoServer(t)
	d := &Docker{Bin: bin}
	conn, err := d.DialContainerPort(context.Background(), "isl", "127.0.0.1", portOf(t, ln))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Exchange a line BEFORE reading the recording. DialContainerPort returns as
	// soon as the process starts, so reading the file straight away races the
	// child that writes it — the first version of this control did exactly that
	// and failed with "no such file". A completed round trip proves the child ran.
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(conn, "sync\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatalf("the dial never reached the listener: %v", err)
	}

	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("the stub docker was never invoked: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(b)), "\n")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"exec", "-i", "isl", "bash", "-c", dialScript, "dejima-dial", "127.0.0.1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the dial argv is missing %q — the tests above are not exercising the "+
				"production command.\nargv: %v", want, argv)
		}
	}
	// And the script must be passed as ONE argument, not word-split: a /dev/tcp
	// redirection broken across argv would not be the script at all.
	var sawWholeScript bool
	for _, a := range argv {
		if a == dialScript {
			sawWholeScript = true
		}
	}
	if !sawWholeScript {
		t.Errorf("the dial script was not passed as a single argument.\nargv: %v", argv)
	}
}

// The destination is passed as ARGV, never interpolated into the script text.
// That is what stops a caller-supplied destination becoming a shell command.
//
// THE PAYLOAD MATTERS, and my first one was worthless. `"; touch x; echo "` does
// nothing even against the interpolated form: bash does not re-parse a redirect
// target as syntax, so quotes and semicolons are just filename characters. The
// test passed against the vulnerable code and proved nothing — a hollow security
// test, which is worse than none, because it reads as coverage.
//
// Bash DOES perform expansion on the redirect word, so command substitution is
// the payload that discriminates. Verified by hand against both forms:
//
//	interpolated:  exec 3<>/dev/tcp/$(touch M)127.0.0.1/1   -> M created
//	argv:          exec 3<>/dev/tcp/"$1"/"$2"               -> M not created
func TestDialDestinationIsDataNotCode(t *testing.T) {
	requireBash(t)
	marker := filepath.Join(t.TempDir(), "executed")
	d := stubDockerExec(t)
	conn, err := d.DialContainerPort(context.Background(), "isl",
		"$(touch "+marker+")127.0.0.1", 1)
	if err == nil {
		// Give the child a moment to run before judging; the dial returns at
		// Start(), not at completion.
		<-conn.(*execConn).Reaped()
		_ = conn.Close()
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("a hostile destination executed a command — the dial script must pass the " +
			"destination as argv, never interpolate it into the script text")
	}
}
