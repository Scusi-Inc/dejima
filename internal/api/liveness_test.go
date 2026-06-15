package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
)

// tmuxHook simulates the tmux probes agentLiveness issues: has-session always
// succeeds; pane_current_command returns paneCmd.
func tmuxHook(paneCmd string) func([]string) (string, string, int, bool) {
	return func(cmd []string) (string, string, int, bool) {
		joined := strings.Join(cmd, " ")
		switch {
		case strings.Contains(joined, "has-session"):
			return "", "", 0, true // session exists
		case strings.Contains(joined, "pane_current_command"):
			return paneCmd, "", 0, true
		}
		return "", "", 0, false
	}
}

// TestAgentLiveness covers the agent-process liveness verdict on the detail
// endpoint: a live session whose foreground is a shell is "exited" for an
// interactive agent, "running" for the shell agent type and for a real process.
func TestAgentLiveness(t *testing.T) {
	cases := []struct {
		name      string
		agentType string
		paneCmd   string
		want      string
	}{
		{"agent-alive", "claude-code", "node", "running"},
		{"agent-died-to-shell", "claude-code", "bash", "exited"},
		{"login-shell-dash", "codex", "-zsh", "exited"},
		{"shell-agent-is-healthy", handlers.Shell, "bash", "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, f := newTestServer(t)
			f.execHook = tmuxHook(tc.paneCmd)
			p := &project.Project{
				Name:         "isl",
				DesiredState: project.StateRunning,
				Agents: []project.AgentSpec{
					{ID: "a1", Type: tc.agentType, Tmux: "agent-a1", Worktree: "/workspace"},
				},
			}
			if err := p.Save(); err != nil {
				t.Fatal(err)
			}

			rr := do(t, h, http.MethodGet, "/v1/islands/isl", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("detail: %d %s", rr.Code, rr.Body.String())
			}
			var info IslandInfo
			if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			if len(info.Agents) != 1 {
				t.Fatalf("got %d agents, want 1", len(info.Agents))
			}
			if got := info.Agents[0].State; got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAgentLivenessStoppedSession: no tmux session → "stopped", and the pane
// probe is never even consulted.
func TestAgentLivenessStoppedSession(t *testing.T) {
	h, f := newTestServer(t)
	f.execHook = func(cmd []string) (string, string, int, bool) {
		if strings.Contains(strings.Join(cmd, " "), "has-session") {
			return "", "", 1, true // no session
		}
		return "", "", 0, false
	}
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents:       []project.AgentSpec{{ID: "a1", Type: "claude-code", Tmux: "agent-a1"}},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	rr := do(t, h, http.MethodGet, "/v1/islands/isl", "")
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Agents[0].State != "stopped" {
		t.Errorf("state = %q, want stopped", info.Agents[0].State)
	}
}
