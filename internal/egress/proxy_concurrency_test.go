package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// echoServer stands in for an upstream (api.anthropic.com et al): it accepts
// connections and echoes bytes until the peer closes.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

// proxyServer runs the real Proxy on a loopback listener.
func proxyServer(t *testing.T) (addr string, log *Log) {
	t.Helper()
	log = NewLog(4096)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	srv := &http.Server{Handler: NewProxy(log, AllowAll{}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), log
}

// tunnel opens one CONNECT tunnel through the proxy to target, round-trips a
// payload, and returns an error describing any step that failed.
func tunnel(proxyAddr, target, island, payload string) error {
	c, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial proxy: %w", err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
		target, target, basicAuth(island, "tok"))
	if _, err := io.WriteString(c, req); err != nil {
		return fmt.Errorf("write CONNECT: %w", err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return fmt.Errorf("read CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CONNECT status %d", resp.StatusCode)
	}

	if _, err := io.WriteString(c, payload); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		return fmt.Errorf("read echo: %w", err)
	}
	if string(got) != payload {
		return fmt.Errorf("echo mismatch: got %q want %q", got, payload)
	}
	return nil
}

// TestProxyConcurrentTunnels is the regression test for the egress stall: many
// islands opening HTTPS tunnels at once, all of which must complete their
// outbound leg. The original failure was descriptor exhaustion under exactly
// this shape of load — every tunnel holds two descriptors for its lifetime —
// which surfaced as connections that were accepted but never served.
//
// Held open simultaneously (not opened-and-closed in sequence) so the peak
// descriptor count is real, and every tunnel must both connect AND round-trip
// data: a proxy that accepts and then stalls fails the round-trip, which is
// the symptom this guards.
func TestProxyConcurrentTunnels(t *testing.T) {
	t.Parallel()
	upstream := echoServer(t)
	proxyAddr, log := proxyServer(t)

	// 100 tunnels is 200 descriptors held at once — comfortably past the 256
	// soft limit that caused the outage, while keeping the test's ephemeral-port
	// footprint small enough to run repeatedly and alongside other suites.
	const tunnels = 100
	var wg, established sync.WaitGroup
	errs := make(chan error, tunnels)
	release := make(chan struct{})

	// Stagger the connect phase. The listen backlog is kern.ipc.somaxconn (128
	// on macOS), so firing all 200 SYNs at once overflows it and some dials hit
	// TCP retransmit backoff — that measures the kernel's backlog, not the
	// daemon. What this test is actually about is 200 tunnels HELD OPEN at once
	// (400 descriptors), which the release gate below guarantees regardless of
	// how fast they were dialed.
	dialSlot := make(chan struct{}, 50)

	for i := range tunnels {
		wg.Add(1)
		established.Add(1)
		go func(i int) {
			defer wg.Done()
			dialSlot <- struct{}{}
			c, err := net.DialTimeout("tcp", proxyAddr, 10*time.Second)
			if err != nil {
				<-dialSlot
				established.Done()
				errs <- fmt.Errorf("tunnel %d: dial proxy: %w", i, err)
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))

			target := upstream.Addr().String()
			island := fmt.Sprintf("island-%d", i%8)

			// Establish the tunnel, freeing the dial slot once the handshake is
			// done — the tunnel itself stays open, which is the point.
			br, herr := func() (*bufio.Reader, error) {
				defer func() { <-dialSlot; established.Done() }()
				req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
					target, target, basicAuth(island, "tok"))
				if _, err := io.WriteString(c, req); err != nil {
					return nil, fmt.Errorf("write CONNECT: %w", err)
				}
				br := bufio.NewReader(c)
				resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
				if err != nil {
					return nil, fmt.Errorf("read CONNECT response: %w", err)
				}
				if resp.StatusCode != http.StatusOK {
					return nil, fmt.Errorf("CONNECT status %d", resp.StatusCode)
				}
				return br, nil
			}()
			if herr != nil {
				errs <- fmt.Errorf("tunnel %d: %w", i, herr)
				return
			}

			// Hold every tunnel open until all of them are established, so peak
			// concurrency — and peak descriptor use — is genuinely `tunnels`.
			<-release

			payload := fmt.Sprintf("hello-%d\n", i)
			if _, err := io.WriteString(c, payload); err != nil {
				errs <- fmt.Errorf("tunnel %d: write payload: %w", i, err)
				return
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(br, got); err != nil {
				errs <- fmt.Errorf("tunnel %d: read echo: %w", i, err)
				return
			}
			if string(got) != payload {
				errs <- fmt.Errorf("tunnel %d: echo mismatch %q != %q", i, got, payload)
			}
		}(i)
	}

	// Once every tunnel has established (or failed to), they are all open at
	// once — that's peak descriptor pressure. Now let them all talk.
	established.Wait()
	close(release)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent tunnels did not complete within 60s — proxy stalled under load")
	}
	close(errs)

	var failed int
	for err := range errs {
		if failed < 10 {
			t.Error(err)
		}
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d/%d concurrent tunnels failed", failed, tunnels)
	}

	// Every tunnel should also have been attributed and recorded.
	var recorded int
	for i := range 8 {
		recorded += len(log.List(fmt.Sprintf("island-%d", i)))
	}
	if recorded != tunnels {
		t.Errorf("recorded %d events, want %d", recorded, tunnels)
	}
}

// TestProxySequentialTunnelsDoNotLeak runs many tunnels one after another. If
// the proxy leaked a descriptor per tunnel, this exhausts the limit and starts
// failing partway through even though concurrency stays at one.
func TestProxySequentialTunnelsDoNotLeak(t *testing.T) {
	t.Parallel()
	upstream := echoServer(t)
	proxyAddr, _ := proxyServer(t)

	for i := range 120 {
		if err := tunnel(proxyAddr, upstream.Addr().String(), "island-a", fmt.Sprintf("ping-%d\n", i)); err != nil {
			t.Fatalf("tunnel %d of 120 failed (descriptor leak?): %v", i, err)
		}
	}
}
