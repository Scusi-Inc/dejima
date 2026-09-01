package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

func updIsland(t *testing.T, agentType string) (http.Handler, *fakeRuntime) {
	t.Helper()
	h, f := newTestServer(t)
	p := &project.Project{
		Name: "isl", DesiredState: project.StateRunning,
		Agents: []project.AgentSpec{{ID: "a1", Type: agentType, Tmux: "agent-a1"}},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	return h, f
}

// Updating must RELAUNCH, not just install.
//
// Every self-installing agent launches with `command -v X || install X`, so an
// island pins whatever version it first installed. Installing the new package
// while the old process keeps running changes the version on disk and not the
// one in memory — every surface would report the new one while the island ran
// the old.
func TestAgentUpdateInstallsAndRelaunches(t *testing.T) {
	h, f := updIsland(t, "openclaw")
	f.mu.Lock()
	f.execs = nil
	f.mu.Unlock()

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/agents/a1/update", `{}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	var out UpdateAgentResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Command, "openclaw@latest") {
		t.Errorf("command = %q, want the pinned-latest install — plain `npm install -g "+
			"openclaw` is what the launch line already does, and it is install-if-missing", out.Command)
	}
	if !out.Restarted {
		t.Error("reported not relaunched; the new version would be on disk with the old " +
			"one still running")
	}

	f.mu.Lock()
	execs := append([][]string(nil), f.execs...)
	f.mu.Unlock()
	var ranInstall, killedSession bool
	for _, c := range execs {
		j := strings.Join(c, " ")
		if strings.Contains(j, "openclaw@latest") {
			ranInstall = true
		}
		if strings.Contains(j, "kill-session") && strings.Contains(j, "agent-a1") {
			killedSession = true
		}
	}
	if !ranInstall {
		t.Errorf("the update command never ran in the container: %v", execs)
	}
	if !killedSession {
		t.Errorf("the agent was never relaunched, so it is still the old process: %v", execs)
	}
}

// A BUNDLED agent is not "unupdatable" — its version is the island image's, and
// the reply has to say which command actually updates it. A vague failure sends
// the operator to `dejima exec` and a hand-typed install into an image-managed
// path, which the next upgrade silently reverts.
func TestBundledAgentIsToldToUpgradeTheImage(t *testing.T) {
	h, _ := updIsland(t, "claude-code")
	rr := do(t, h, http.MethodPost, "/v1/islands/isl/agents/a1/update", `{}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("update of a bundled agent: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "dejima upgrade") {
		t.Errorf("the refusal does not name the command that DOES update it: %s", rr.Body.String())
	}
}

// A failed install must return its output. An exit code alone sends the operator
// back in with `dejima exec` to find out what every install failure already said
// in its own last lines.
func TestFailedUpdateReturnsItsOutput(t *testing.T) {
	h, f := updIsland(t, "openclaw")
	f.mu.Lock()
	f.execHook = func(cmd []string) (string, string, int, bool) {
		if strings.Contains(strings.Join(cmd, " "), "openclaw@latest") {
			return "", "npm ERR! 403 Forbidden - GET https://registry.npmjs.org/openclaw", 1, true
		}
		return "", "", 0, false
	}
	f.mu.Unlock()

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/agents/a1/update", `{}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("failed update: %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "403 Forbidden") {
		t.Errorf("the failure output was dropped, leaving only an exit code: %s", rr.Body.String())
	}
}
