package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// COUNT THE DIALS. On the WSL transport every dial forks a wsl.exe + socat pair,
// so a connection that cannot be reused is a subprocess created and destroyed
// per request. The dashboard polls several endpoints on a tick, and the operator
// hit WSAENOBUFS (Wsl/Service/0x80072747) three times in one evening on a
// freshly installed distro, twice wedging the WSL service itself.
//
// Go's transport only returns a connection to the pool when the body has been
// read to EOF and closed. doVia left it unread on three paths: when no output is
// expected at all, when the JSON decoder stops at the end of the value and
// leaves trailing bytes, and on the error path. Each one silently downgraded
// keep-alive to connection-per-request.
func TestConnectionsAreReusedAcrossRequests(t *testing.T) {
	var dials int64

	// A trailing newline after the JSON is the ordinary case — encoders add one —
	// and it is enough on its own to leave the body unread at EOF.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]string{"ok": "yes"})
		_, _ = w.Write(append(b, '\n'))
	}))
	defer srv.Close()

	c := &Client{
		base: srv.URL,
		httpc: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					atomic.AddInt64(&dials, 1)
					return (&net.Dialer{}).DialContext(ctx, network, addr)
				},
				MaxIdleConns:        4,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}

	const requests = 10
	for i := 0; i < requests; i++ {
		var out map[string]string
		if err := c.do(context.Background(), http.MethodGet, "/v1/thing", nil, &out); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	if n := atomic.LoadInt64(&dials); n > 1 {
		t.Errorf("%d requests opened %d connections; the pool is not being reused. "+
			"On the WSL transport each of those is a wsl.exe + socat subprocess, which is "+
			"how WSAENOBUFS happens.", requests, n)
	}
}

// The no-output path is the one most likely to be missed: nothing reads the body
// because nothing wants it, so it is never drained and the connection dies.
func TestConnectionsAreReusedWhenNoOutputIsExpected(t *testing.T) {
	var dials int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ignored":true}` + "\n"))
	}))
	defer srv.Close()

	c := &Client{
		base: srv.URL,
		httpc: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				atomic.AddInt64(&dials, 1)
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			MaxIdleConns: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 60 * time.Second,
		}},
	}

	for i := 0; i < 10; i++ {
		if err := c.do(context.Background(), http.MethodPost, "/v1/act", nil, nil); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if n := atomic.LoadInt64(&dials); n > 1 {
		t.Errorf("10 no-output requests opened %d connections; an undrained body is not reusable", n)
	}
}

// A CONCURRENT BURST WIDER THAN THE POOL dials connections it will immediately
// throw away. Go pools MaxIdleConnsPerHost; anything above that in one burst is
// dialled fresh and closed on return, because the pool is already full.
//
// On WSL each of those is a wsl.exe + socat subprocess. The dashboard polls
// several endpoints on one tick, so a burst wider than the pool churns a
// subprocess per excess request EVERY TICK — no matter how perfectly the bodies
// are drained. Draining fixes reuse; it does not fix a burst.
//
// Raising the pool is the wrong lever: each idle connection HOLDS a subprocess,
// and the exhaustion being chased (WSAENOBUFS from wslservice.exe) is about
// handles and kernel pool rather than ephemeral ports.
func TestConcurrentBurstDoesNotExceedThePool(t *testing.T) {
	var dials, inFlight, maxInFlight int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&inFlight, 1)
		for {
			m := atomic.LoadInt64(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt64(&maxInFlight, m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // hold the connection so bursts overlap
		atomic.AddInt64(&inFlight, -1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":"yes"}` + "\n"))
	}))
	defer srv.Close()

	c := &Client{
		base: srv.URL,
		httpc: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				atomic.AddInt64(&dials, 1)
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			MaxIdleConns: wslMaxConns, MaxIdleConnsPerHost: wslMaxConns,
			IdleConnTimeout: 60 * time.Second,
		}},
		dialSem: make(chan struct{}, wslMaxConns),
	}

	// Three ticks of eight concurrent requests — wider than the pool, which is
	// the shape the dashboard actually produces.
	for tick := 0; tick < 3; tick++ {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var out map[string]string
				_ = c.do(context.Background(), http.MethodGet, "/v1/thing", nil, &out)
			}()
		}
		wg.Wait()
	}

	if n := atomic.LoadInt64(&maxInFlight); n > wslMaxConns {
		t.Errorf("%d requests were in flight at once against a pool of %d; the excess are "+
			"dialled and discarded — on WSL that is a subprocess per excess request per tick",
			n, wslMaxConns)
	}
	if n := atomic.LoadInt64(&dials); n > wslMaxConns {
		t.Errorf("24 requests over 3 bursts opened %d connections; at most %d should ever "+
			"be dialled", n, wslMaxConns)
	}
}
