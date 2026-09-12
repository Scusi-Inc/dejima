package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/project"
)

// A rename has to reach the tabs that are ALREADY open. Renaming an island set
// its Title and stopped there: new tabs the dashboard opened read the new name
// while every attached session kept the old one, so the same island appeared
// under two names at once and neither was wrong-looking enough to explain.
//
// These cover the composition rule and the push. The push test's control is a
// session attached to a DIFFERENT island: without one, a broadcast that wrote to
// every registered connection would pass exactly the same way.

func TestIslandTabTitle(t *testing.T) {
	p := &project.Project{
		Name:  "playbook-internal",
		Title: "playbook",
		Agents: []project.AgentSpec{
			{ID: "a1", Label: "diagramme"},
			{ID: "a2"},
		},
	}
	bare := &project.Project{
		Name:   "wildfire",
		Agents: []project.AgentSpec{{ID: "a1", Label: "manager"}},
	}

	for _, tc := range []struct {
		name      string
		p         *project.Project
		agentID   string
		showAgent bool
		want      string
	}{
		{"title wins over slug", p, "a1", true, "playbook/diagramme"},
		{"unlabelled agent falls back to its id", p, "a2", true, "playbook/a2"},
		{"a bare island attach shows no agent", p, "a2", false, "playbook"},
		{"a labelled agent shows even on a bare attach", p, "a1", false, "playbook/diagramme"},
		{"no title falls back to the slug", bare, "a1", true, "wildfire/manager"},
		{"an unknown agent degrades to the island", p, "gone", true, "playbook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := islandTabTitle(tc.p, tc.agentID, tc.showAgent); got != tc.want {
				t.Errorf("islandTabTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

// dialRegistered opens a websocket whose server end is registered against
// (island, agentID) and returns the client end plus a wait for registration.
func dialRegistered(t *testing.T, s *Server, island, agentID string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		h := s.registerSessionConn(conn, island, agentID, true)
		defer s.unregisterSessionConn(h)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusInternalError, "") })
	return conn
}

func TestBroadcastIslandTitle_ReachesAttachedSessions(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	renamed := dialRegistered(t, s, "playbook-internal", "a1")
	other := dialRegistered(t, s, "wildfire", "a1") // control: a different island

	deadline := time.Now().Add(5 * time.Second)
	for s.attachedConnCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("sessions never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	p := &project.Project{
		Name:   "playbook-internal",
		Title:  "playbook",
		Agents: []project.AgentSpec{{ID: "a1", Label: "diagramme"}},
	}
	s.broadcastIslandTitle(context.Background(), p)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := renamed.Read(ctx)
	if err != nil {
		t.Fatalf("renamed island's session read: %v", err)
	}
	var env SessionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "title" || env.Title != "playbook/diagramme" {
		t.Fatalf("got %+v, want a title envelope carrying %q", env, "playbook/diagramme")
	}

	// The control must NOT have been written to. A short read deadline is the
	// only way to assert "nothing arrived"; it expiring is the pass.
	quiet, cancelQuiet := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelQuiet()
	if _, got, rerr := other.Read(quiet); rerr == nil {
		t.Fatalf("a session on another island received %q", got)
	}
}

// The status bar inside the island reads DEJIMA_PROJECT_TITLE, which the daemon
// seeds at container create and pushes on rename. The env name is a contract
// between two files that are never edited together; when it drifted, the rename
// simply had no visible effect and looked like a tmux problem.
func TestTmuxConfReadsTheDisplayTitle(t *testing.T) {
	conf, err := os.ReadFile("../../image/tmux.conf")
	if err != nil {
		t.Fatalf("read image/tmux.conf: %v", err)
	}
	var statusLeft string
	for _, line := range strings.Split(string(conf), "\n") {
		if strings.HasPrefix(line, "set -g status-left ") {
			statusLeft = line
		}
	}
	if statusLeft == "" {
		t.Fatal("image/tmux.conf sets no status-left")
	}
	if !strings.Contains(statusLeft, "DEJIMA_PROJECT_TITLE") {
		t.Errorf("status-left does not read DEJIMA_PROJECT_TITLE, so a rename shows nothing "+
			"inside the island: %s", statusLeft)
	}
	// The slug must stay the fallback: an island with no title still needs a name.
	if !strings.Contains(statusLeft, "DEJIMA_PROJECT_NAME") {
		t.Errorf("status-left dropped the DEJIMA_PROJECT_NAME fallback, so an untitled "+
			"island shows nothing: %s", statusLeft)
	}
}
