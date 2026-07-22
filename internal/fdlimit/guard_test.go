//go:build !windows

package fdlimit

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// stubListener returns a canned error from Accept. Inducing real EMFILE in a
// test is unreliable — the client's own dial needs a descriptor too, so the
// connection never arrives and Accept blocks — and the behaviour under test is
// the guard's classification and reporting, not the kernel's.
type stubListener struct{ err error }

func (s stubListener) Accept() (net.Conn, error) { return nil, s.err }
func (s stubListener) Close() error              { return nil }
func (s stubListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7280} }

func TestGuardReportsExhaustion(t *testing.T) {
	// The shape net.Listener actually returns: a *net.OpError wrapping the
	// syscall errno. A guard that only matched a bare errno would miss it.
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}

	var msgs []string
	g := Guard(stubListener{err: emfile}, func(msg string, _ ...any) { msgs = append(msgs, msg) })

	if _, err := g.Accept(); !errors.Is(err, syscall.EMFILE) {
		t.Fatalf("Accept error = %v, want EMFILE passed through", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d warnings, want 1", len(msgs))
	}
	if msgs[0] == "" {
		t.Error("warning message is empty")
	}

	// Under exhaustion accept fails in a tight loop; the guard must not turn
	// that into a log flood.
	for range 500 {
		_, _ = g.Accept()
	}
	if len(msgs) != 1 {
		t.Errorf("got %d warnings after 501 failed accepts, want 1 (rate-limited)", len(msgs))
	}
}

func TestGuardIgnoresOrdinaryErrors(t *testing.T) {
	other := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.ECONNABORTED)}

	var warned int
	g := Guard(stubListener{err: other}, func(string, ...any) { warned++ })
	if _, err := g.Accept(); err == nil {
		t.Fatal("expected the underlying error to pass through")
	}
	if warned != 0 {
		t.Errorf("warned %d times on a non-exhaustion error, want 0", warned)
	}
}

// TestGuardPassesConnectionsThrough: the guard must be transparent in the
// normal case — it sits in front of the busiest listener in the daemon.
func TestGuardPassesConnectionsThrough(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var warned int
	g := Guard(ln, func(string, ...any) { warned++ })

	dialErr := make(chan error, 1)
	go func() {
		c, derr := net.Dial("tcp", g.Addr().String())
		dialErr <- derr
		if derr == nil {
			_, _ = c.Write([]byte("ok"))
			c.Close()
		}
	}()

	// Bound the accept: if the dial can't get an ephemeral port (a saturated
	// host), nothing ever arrives and an unbounded Accept would hang the suite.
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))

	conn, err := g.Accept()
	if err != nil {
		if derr := <-dialErr; derr != nil {
			t.Skipf("could not dial loopback in this environment: %v", derr)
		}
		t.Fatalf("Accept: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "ok" {
		t.Errorf("read %q, want %q", buf, "ok")
	}
	if warned != 0 {
		t.Errorf("warned %d times on a healthy listener, want 0", warned)
	}
}
