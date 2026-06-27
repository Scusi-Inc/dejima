package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/egress"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/runtime"
)

func TestIslandEgressReadAPI(t *testing.T) {
	// Build the server directly (newTestServer discards the *Server, and we need
	// it to call EnableEgress).
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := srv.Handler()

	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"alpha","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: got %d", rr.Code)
	}

	// Proxy disabled (default): the endpoint still works and returns an empty,
	// non-null list — clients render "nothing yet", not an error.
	rr := do(t, h, http.MethodGet, "/v1/islands/alpha/egress", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("egress (disabled): got %d, want 200", rr.Code)
	}
	var empty EgressEventsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if empty.Events == nil || len(empty.Events) != 0 {
		t.Fatalf("want empty non-nil events when disabled, got %+v", empty.Events)
	}

	// Enable egress with a log and record events → the API surfaces them for the
	// right island only.
	log := egress.NewLog(8)
	srv.EnableEgress("host.docker.internal:7280", log)
	log.Record(egress.Event{Island: "alpha", Host: "api.anthropic.com", Port: "443", Method: http.MethodConnect, Decision: egress.DecisionAllow, Time: time.Now().UTC()})
	log.Record(egress.Event{Island: "beta", Host: "elsewhere.example", Port: "443", Decision: egress.DecisionAllow})

	rr = do(t, h, http.MethodGet, "/v1/islands/alpha/egress", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("egress: got %d", rr.Code)
	}
	var got EgressEventsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].Host != "api.anthropic.com" {
		t.Fatalf("want alpha's single egress event, got %+v", got.Events)
	}
	if got.Events[0].Decision != egress.DecisionAllow {
		t.Errorf("decision = %q, want allow", got.Events[0].Decision)
	}
}

func TestEgressProxyEnvInjection(t *testing.T) {
	env := egress.ProxyEnv("alpha", "host.docker.internal:7280")
	want := "http://alpha:x@host.docker.internal:7280"
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	// Loopback + the daemon host must bypass so the autonomy path doesn't loop.
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if env[k] == "" || !strings.Contains(env[k], "host.docker.internal") || !strings.Contains(env[k], "127.0.0.1") {
			t.Errorf("%s missing bypass entries: %q", k, env[k])
		}
	}
}
