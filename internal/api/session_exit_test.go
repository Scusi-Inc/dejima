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

// serveTmuxWS must send an explicit {"type":"exit"} envelope when the bridged
// terminal process ends, so the client stops instead of reconnecting forever.
// This is the server half of the operator-trapping fix (host-terminal open
// failure + `dejima connect` then `exit`). Uses a real short-lived PTY via
// HostPTY (a command that exits on its own); skipped where no PTY is available.
func TestServeTmuxWS_SendsExitOnTerminalEnd(t *testing.T) {
	// Probe PTY availability up front so the assertion below is unambiguous.
	if probe, err := bridge.HostPTY(context.Background(), []string{"true"}, 0, 0, bridge.TermEnv{}); err != nil {
		t.Skipf("no PTY available in this environment: %v", err)
	} else {
		probe.Close()
	}

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	attach := func(ctx context.Context, rows, cols uint16, te bridge.TermEnv) (*bridge.PTYSession, error) {
		// A terminal that prints once and exits — mimics a shell `exit` / an
		// instant open-failure: the PTY reaches EOF while the ws is healthy.
		return bridge.HostPTY(ctx, []string{"sh", "-c", "echo bye; exit 0"}, rows, cols, te)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serveTmuxWS(w, r, "test-term", "k", attach, nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sawExit := false
	for {
		_, data, rerr := conn.Read(ctx)
		if rerr != nil {
			break // connection closed; stop reading
		}
		var env SessionEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.Type == "exit" {
			sawExit = true
			break
		}
	}
	if !sawExit {
		t.Fatal("serveTmuxWS did not send an exit envelope when the terminal ended")
	}
}
