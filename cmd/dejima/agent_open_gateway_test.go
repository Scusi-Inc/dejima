package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// waitForForward proves ssh bound the LOCAL end. gatewayReady proves something
// inside the island answers on the far end. The gap between those two is a
// browser tab opened onto a port that accepts and immediately closes, which is
// what an openclaw agent looks like for the several minutes its first launch
// spends in `npm install -g openclaw`.
//
// The two fixtures below are the whole test: a real HTTP-ish server for
// "gateway present", and a listener that accepts and closes without writing for
// "ssh accepted locally, remote dial failed". The second one is the case the
// old code called ready.

// servingListener answers one byte of an HTTP response, like any real server.
func servingListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n"))
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// acceptAndCloseListener is ssh with a dead remote: the local end accepts, then
// the connection dies with nothing written.
func acceptAndCloseListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestGatewayReadyWhenSomethingServes(t *testing.T) {
	if !gatewayReady(context.Background(), servingListener(t)) {
		t.Error("a listener that answers must count as a live gateway")
	}
}

// The bug, stated as a test: this is precisely the state waitForForward calls
// ready. A 401 counts as up and this must not, or the distinction is worthless.
func TestGatewayNotReadyWhenTheForwardAcceptsAndCloses(t *testing.T) {
	if gatewayReady(context.Background(), acceptAndCloseListener(t)) {
		t.Error("accept-then-close is ssh reporting a failed remote dial, not a gateway")
	}
}

func TestGatewayNotReadyWhenNothingListens(t *testing.T) {
	if gatewayReady(context.Background(), freePort(t)) {
		t.Error("a closed port is not a gateway")
	}
}

// The control on the two above. They only mean something if the OLD notion of
// readiness cannot tell the fixtures apart — if waitForForward rejected
// accept-and-close too, gatewayReady would be redundant and these tests would be
// asserting a distinction that already existed.
//
// So: assert waitForForward calls BOTH fixtures ready. That is the gap, measured
// rather than asserted in a comment.
func TestWaitForForwardCannotTellTheFixturesApart(t *testing.T) {
	for name, port := range map[string]int{
		"serving":          servingListener(t),
		"accept-and-close": acceptAndCloseListener(t),
	} {
		if err := waitForForward(context.Background(), port, make(chan struct{}), 2*time.Second); err != nil {
			t.Fatalf("waitForForward(%s) = %v; if it now rejects accept-and-close, "+
				"gatewayReady is redundant and the tests above no longer describe a real gap", name, err)
		}
	}
}

func TestWaitForGatewayReturnsWhenItComesUp(t *testing.T) {
	if !waitForGateway(context.Background(), servingListener(t), make(chan struct{}), 5*time.Second, nil) {
		t.Error("a serving gateway should be reported up")
	}
}

// ssh dying while we wait must end the wait — otherwise `agent open` sits for
// the full budget on a tunnel that no longer exists.
func TestWaitForGatewayStopsWhenSSHExits(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	start := time.Now()
	if waitForGateway(context.Background(), freePort(t), exited, time.Minute, nil) {
		t.Error("no gateway can be up once ssh is gone")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("waited %s for a dead ssh; it should return promptly", time.Since(start))
	}
}

func TestWaitForGatewayHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForGateway(ctx, freePort(t), make(chan struct{}), time.Minute, nil) {
		t.Error("a cancelled context should not report a live gateway")
	}
}

func TestWaitForGatewayTimesOut(t *testing.T) {
	if waitForGateway(context.Background(), freePort(t), make(chan struct{}), 100*time.Millisecond, nil) {
		t.Error("an absent gateway should not be reported up")
	}
}

// The wait can last minutes, so it has to say what it is waiting for on the
// first miss. A silent multi-minute wait reads as a hang, and an operator who
// Ctrl-Cs a working npm install is left with a half-installed agent — the
// failure compounding rather than resolving.
func TestWaitForGatewayAnnouncesTheWaitExactlyOnce(t *testing.T) {
	calls := 0
	waitForGateway(context.Background(), freePort(t), make(chan struct{}), 2500*time.Millisecond,
		func() { calls++ })
	if calls != 1 {
		t.Errorf("notify called %d times over a multi-attempt wait, want exactly 1 "+
			"(silent = looks like a hang; repeated = noise)", calls)
	}
}

// ...and must NOT announce anything when the gateway is already up, which is the
// common case. A warning printed on every healthy run is one nobody reads.
func TestWaitForGatewayStaysQuietWhenTheGatewayIsAlreadyUp(t *testing.T) {
	calls := 0
	if !waitForGateway(context.Background(), servingListener(t), make(chan struct{}), 5*time.Second,
		func() { calls++ }) {
		t.Fatal("gateway should be up")
	}
	if calls != 0 {
		t.Errorf("notify fired %d times on a healthy gateway, want 0", calls)
	}
}
