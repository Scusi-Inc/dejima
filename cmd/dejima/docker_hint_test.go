package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// Island creation must STOP when the daemon says Docker is down, rather than
// running a build that cannot succeed and reporting docker's socket error —
// which names a path on a machine the operator is not sitting at.
func TestCreateStopsWhenDaemonDockerIsDown(t *testing.T) {
	var mu sync.Mutex
	var hit []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hit = append(hit, r.URL.Path)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "overview") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.OverviewResponse{
				DockerReachable:    false,
				IslandImagePresent: false,
			})
			return
		}
		// Any other call means we went ahead with work we already knew would
		// fail. Answer 500 so the test fails loudly rather than hanging.
		http.Error(w, "should not have been called", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c, err := api.NewTCPClient(ts.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	// buildRequest indexes agents[0], so give it the minimum a real creator has.
	m := &creatorModel{client: c, nameInput: "burlingame",
		agents: []api.AgentSpecRequest{{Type: "claude"}}}
	msg, _ := m.createCmd()().(islandCreatedMsg)
	if msg.err == nil {
		t.Fatal("creation reported success while the daemon had no Docker")
	}

	got := msg.err.Error()
	// The whole point is telling them WHICH machine. A message that just says
	// "Docker isn't running" sends someone to check the laptop in front of them.
	if !strings.Contains(got, "not on this one") {
		t.Errorf("error does not say the host is a different machine:\n%s", got)
	}
	// sudo was a real reaction to the old message and it cannot help.
	if !strings.Contains(got, "sudo") {
		t.Errorf("error does not head off sudo, which was actually tried:\n%s", got)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range hit {
		if strings.Contains(p, "image") || strings.Contains(p, "islands") {
			t.Errorf("called %q after learning Docker was down; it must stop first (all calls: %v)", p, hit)
		}
	}
}

// The reverse: a healthy daemon must not be blocked by the new check.
func TestCreateProceedsWhenDockerIsReachable(t *testing.T) {
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "overview") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.OverviewResponse{
				DockerReachable:    true,
				IslandImagePresent: true, // skip the build, we only care that we got past the gate
			})
			return
		}
		reached = true
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c, err := api.NewTCPClient(ts.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	m := &creatorModel{client: c, nameInput: "burlingame",
		agents: []api.AgentSpecRequest{{Type: "claude"}}}
	msg, _ := m.createCmd()().(islandCreatedMsg)
	if !reached {
		t.Fatalf("a reachable-Docker daemon never got past the preflight (err: %v)", msg.err)
	}
	if msg.err != nil && strings.Contains(msg.err.Error(), "Docker isn't running") {
		t.Errorf("reported Docker down when the daemon said it was up:\n%s", msg.err)
	}
}
