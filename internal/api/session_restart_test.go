package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/bridge"
)

// attachedConnCount reports how many live session websockets are registered for
// the restart broadcast (test-only accessor for the unexported registry).
func (s *Server) attachedConnCount() int {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return len(s.sessionConns)
}

// CloseSessionsForRestart must close every attached session websocket with the
// Service-Restart close code (1012) — the reconnect-triggering signal the client
// distinguishes from a deliberate NormalClosure — and must NOT emit the
// {"type":"exit"} envelope while restarting (which would stop the client). This
// is the server half of "a daemon restart is a reconnect blink, not a drop".
func TestCloseSessionsForRestart_ClosesWithRestartCode(t *testing.T) {
	if probe, err := bridge.HostPTY(context.Background(), []string{"true"}, 0, 0, bridge.TermEnv{}); err != nil {
		t.Skipf("no PTY available in this environment: %v", err)
	} else {
		probe.Close()
	}

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// A long-lived terminal: the PTY does NOT reach EOF on its own, so any close
	// the client sees comes from the restart path, not a natural terminal end.
	attach := func(ctx context.Context, rows, cols uint16, te bridge.TermEnv) (*bridge.PTYSession, error) {
		return bridge.HostPTY(ctx, []string{"sleep", "10"}, rows, cols, te)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serveTmuxWS(w, r, "test-term", "k", attach, nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusInternalError, "")

	// Wait until the handler has registered its conn, then trigger the restart.
	deadline := time.Now().Add(5 * time.Second)
	for s.attachedConnCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("session websocket never registered for restart broadcast")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := s.CloseSessionsForRestart(); n != 1 {
		t.Fatalf("CloseSessionsForRestart closed %d sessions, want 1", n)
	}

	// The client must observe a close with the Service-Restart code, and must
	// never see an exit envelope along the way.
	for {
		_, data, rerr := conn.Read(ctx)
		if rerr != nil {
			if got := websocket.CloseStatus(rerr); got != websocket.StatusServiceRestart {
				t.Fatalf("restart close code = %d, want %d", got, websocket.StatusServiceRestart)
			}
			break
		}
		var env SessionEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.Type == "exit" {
			t.Fatal("received an exit envelope during a restart — client would stop instead of reconnecting")
		}
	}
}

// A connection that registers AFTER a restart has begun must be closed
// immediately with the restart code (it missed the broadcast), so a session that
// races in during shutdown still reconnects rather than hanging.
func TestRegisterSessionConn_ClosesLateJoinerDuringRestart(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Mark a restart in progress with no conns registered yet.
	if n := s.CloseSessionsForRestart(); n != 0 {
		t.Fatalf("expected 0 closed with no sessions, got %d", n)
	}
	if !s.restartInProgress() {
		t.Fatal("restartInProgress should be true after CloseSessionsForRestart")
	}

	// A late joiner: registerSessionConn should close it right away.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		h := s.registerSessionConn(conn)
		defer s.unregisterSessionConn(h)
		// Block on a read; the immediate restart-close should unblock it.
		_, _, _ = conn.Read(r.Context())
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusInternalError, "")

	_, _, rerr := conn.Read(ctx)
	if rerr == nil {
		t.Fatal("late joiner during restart should have been closed")
	}
	if got := websocket.CloseStatus(rerr); got != websocket.StatusServiceRestart {
		t.Fatalf("late-joiner close code = %d, want %d", got, websocket.StatusServiceRestart)
	}
}
