package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// The entrypoint and the attach path must derive the SAME tmux session name.
//
// They didn't. sessionWS defaults an empty AgentSpec.Tmux to "agent-"+ID;
// image/start.sh defaults an empty DEJIMA_TMUX to the literal "agent-a1" — a
// stale first-agent id, wrong for every island whose agents are not named a1.
//
// On a project record with no Tmux, the entrypoint therefore launched the agent
// into "agent-a1" while the daemon attached to "agent-<id>". `tmux new-session
// -A` creates a session that isn't there, so the operator gets a BARE SHELL
// where their agent should be — and the real agent keeps running in a session
// nothing points at. Nothing errors; both halves report success.
//
// This asserts the daemon always sends a name, so start.sh's fallback is
// unreachable rather than merely usually-unused.
func TestPrimaryTmuxNameNeverFallsThroughToTheEntrypointDefault(t *testing.T) {
	h, f := newTestServer(t)
	// A LEGACY project record: an agent with no Tmux, as written before the field
	// was populated. This is the state that reaches start.sh's fallback; a record
	// created today always carries the name.
	if err := (&project.Project{
		Name:         "wildfire",
		Agent:        "claude-code",
		DesiredState: project.StateRunning,
		Agents:       []project.AgentSpec{{ID: "w1", Type: "claude-code"}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status = runtime.StatusStopped // so wake actually recreates the container
	f.mu.Unlock()
	if rr := do(t, h, http.MethodPost, "/v1/islands/wildfire/upgrade", ""); rr.Code >= 400 {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	cr := f.lastCreate
	f.mu.Unlock()
	if cr.Env == nil {
		t.Fatal("no container was created, so this test is asserting nothing — " +
			"the recreate path did not run")
	}

	got := cr.Env["DEJIMA_TMUX"]
	if got == "" {
		t.Fatal("DEJIMA_TMUX is empty, so start.sh falls back to its hardcoded " +
			"\"agent-a1\" while the daemon attaches to \"agent-w1\" — the agent runs " +
			"in a session nothing points at, and attaching creates a bare shell")
	}
	if got != "agent-w1" {
		t.Errorf("DEJIMA_TMUX = %q, want %q — it must match what sessionWS resolves "+
			"for the same agent, or the two halves address different sessions", got, "agent-w1")
	}
	if strings.Contains(got, "a1") {
		t.Errorf("DEJIMA_TMUX = %q — that is the stale first-agent default, not this "+
			"island's agent", got)
	}
}
