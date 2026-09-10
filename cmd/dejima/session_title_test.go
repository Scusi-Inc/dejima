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

// The tab title is written ONCE at attach, so an island renamed afterwards left
// every open tab reading the old name while newly opened tabs read the new one.
// The daemon now re-asserts the title on hello and pushes one on rename; the
// client has to act on both. Its control is the exit envelope in the same
// stream: a dispatch that fired the callback for any envelope would look
// identical from a test that only ever sent a title.
func TestRunOneSessionConn_AppliesServerTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		for _, env := range []api.SessionEnvelope{
			{Type: "hello", Title: "playbook-internal/diagramme"},
			{Type: "title", Title: "playbook/diagramme"},
			{Type: "data", B64: ""},
			{Type: "exit"},
		} {
			data, _ := json.Marshal(env)
			if c.Write(r.Context(), websocket.MessageText, data) != nil {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	pr, pw, _ := os.Pipe()
	defer pr.Close()
	defer pw.Close()

	titles := make(chan string, 8)
	done := make(chan sessReason, 1)
	go func() {
		done <- runOneSessionConn(ctx, conn, int(pr.Fd()),
			make(chan []byte), make(chan struct{}), false, nil,
			func(s string) { titles <- s })
	}()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("runOneSessionConn did not return")
	}
	close(titles)

	var got []string
	for tl := range titles {
		got = append(got, tl)
	}
	want := []string{"playbook-internal/diagramme", "playbook/diagramme"}
	if len(got) != len(want) {
		t.Fatalf("retitled %v, want exactly %v (data/exit envelopes must not retitle)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("retitled %v, want %v", got, want)
		}
	}
}

// tabTitler is what runSessionLoop hands the dispatch. It must stay silent for a
// session that never took the tab title (a non-TTY client writes no OSC), and
// for an empty title — which is what an OLDER daemon sends on every hello.
func TestTabTitler(t *testing.T) {
	for _, tc := range []struct {
		name   string
		titled bool
		title  string
		want   []string
	}{
		{"a titled session applies a title", true, "playbook", []string{"playbook"}},
		{"an untitled session writes nothing", false, "playbook", nil},
		{"an older daemon's empty title is ignored", true, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wrote []string
			tabTitler(tc.titled, func(s string) { wrote = append(wrote, s) })(tc.title)
			if len(wrote) != len(tc.want) {
				t.Fatalf("wrote %v, want %v", wrote, tc.want)
			}
			for i := range tc.want {
				if wrote[i] != tc.want[i] {
					t.Fatalf("wrote %v, want %v", wrote, tc.want)
				}
			}
		})
	}
}
