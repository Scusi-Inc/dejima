package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/api"
)

// When the server signals the bridged terminal ended — an explicit {"type":"exit"}
// envelope — the client must end the session CLEANLY (sessExitClean), never
// sessReconnect. This is the regression for the operator-trapping loops: a
// `dejima connect` shell where you type `exit`, and a host terminal whose tmux
// exits instantly. Both used to be misread as a transport drop and reconnected
// forever (respawning the shell); the exit envelope stops that.
func TestRunOneSessionConn_ExitEnvelopeEndsClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		data, _ := json.Marshal(api.SessionEnvelope{Type: "exit"})
		_ = c.Write(r.Context(), websocket.MessageText, data)
		// Hold the connection open briefly so the client reads the envelope before
		// the close frame — we're asserting the envelope drives the decision, not
		// the close code.
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// A non-terminal fd → no stdin interception; channels that never fire so the
	// only thing that can end the session is the server's exit envelope.
	pr, pw, _ := os.Pipe()
	defer pr.Close()
	defer pw.Close()

	got := make(chan sessReason, 1)
	go func() {
		got <- runOneSessionConn(ctx, conn, int(pr.Fd()),
			make(chan []byte), make(chan struct{}), false, nil)
	}()

	select {
	case r := <-got:
		if r != sessExitClean {
			t.Fatalf("exit envelope: got reason %d, want sessExitClean (%d)", r, sessExitClean)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runOneSessionConn did not return on exit envelope — likely treated as reconnect")
	}
}
