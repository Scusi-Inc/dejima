package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/runtime"
)

func newEventsServer(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	em, err := events.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := joinBackground(t, NewServer(&fakeRuntime{status: runtime.StatusRunning},
		slog.New(slog.NewTextHandler(io.Discard, nil)), em))
	return srv.Handler()
}

// TestWebhookSubscribeEventFilter: an unknown event type is rejected (so a typo
// can't create a silently-dead filter), and a valid scoped subscription
// round-trips its Events filter.
func TestWebhookSubscribeEventFilter(t *testing.T) {
	h := newEventsServer(t)

	// Bad type → 400.
	rr := do(t, h, http.MethodPost, "/v1/events/subscribe",
		`{"url":"https://example.test/hook","events":["container.crahsed"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad event type: got %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}

	// Valid scoped subscription → 201 with the filter persisted.
	rr = do(t, h, http.MethodPost, "/v1/events/subscribe",
		`{"url":"https://example.test/hook","events":["container.crashed","daemon.started"]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("valid subscribe: got %d, want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	var sub events.Subscription
	if err := json.Unmarshal(rr.Body.Bytes(), &sub); err != nil {
		t.Fatal(err)
	}
	if len(sub.Events) != 2 || sub.Events[0] != events.TypeContainerCrashed {
		t.Errorf("sub.Events = %v, want [container.crashed daemon.started]", sub.Events)
	}

	// No filter → all events (empty Events).
	rr = do(t, h, http.MethodPost, "/v1/events/subscribe", `{"url":"https://example.test/all"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("unfiltered subscribe: got %d, want 201", rr.Code)
	}
	var allSub events.Subscription
	_ = json.Unmarshal(rr.Body.Bytes(), &allSub)
	if len(allSub.Events) != 0 {
		t.Errorf("unfiltered sub.Events = %v, want empty", allSub.Events)
	}
}
