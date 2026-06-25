package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The watch route is a live SSE stream: a 2xx with text/event-stream that the
// client cancels via context (not a buffered one-shot 200).
func TestWatchActionsStreams(t *testing.T) {
	h, _ := newTestServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/link/actions/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/link/actions/watch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drains until ctx cancels the server stream
}
