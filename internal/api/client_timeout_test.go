package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The default client keeps its 30s safety cap; the long-op client has NONE, so a
// caller's (longer) context deadline governs instead of being silently truncated
// to 30s. This is the invariant behind the clone/panic timeout fix — lock it.
func TestClientTimeouts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, err := NewUnixClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.httpc.Timeout != 30*time.Second {
		t.Errorf("default client timeout = %v, want 30s", c.httpc.Timeout)
	}
	if lc := c.longHTTPClient(); lc.Timeout != 0 {
		t.Errorf("long client must have no fixed timeout (got %v) so the context governs", lc.Timeout)
	}
}

// doLong must return at the caller's context deadline, proving the context — not
// a fixed client timeout — bounds the request.
func TestDoLongHonorsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	}))
	defer srv.Close()

	c, err := NewTCPClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := c.doLong(ctx, http.MethodGet, "/v1/hang", nil, nil); err == nil {
		t.Fatal("expected a deadline error from doLong")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("doLong took %v — it isn't honoring the 200ms context deadline", elapsed)
	}
}
