package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// twoAgentServer stands up a running two-agent island: a primary on /workspace
// and one co-located agent in its own worktree. Two agents is the minimum that
// exposes the reconcile gap — with one, there is nothing for the daemon to
// restore and the entrypoint alone looks sufficient.
func twoAgentServer(t *testing.T) (http.Handler, *fakeRuntime) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents: []project.AgentSpec{
			{ID: "a1", Type: "claude-code", Tmux: "dejima", Worktree: "/workspace"},
			{ID: "a2", Type: "claude-code", Tmux: "agent-a2", Worktree: "/workspace/.agents/a2"},
		},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	return srv.Handler(), f
}

// waitForExec polls the fake's recorded exec calls for one satisfying match,
// since reconcileAgents runs on a goroutine. Returns the matching call, or nil.
func waitForExec(f *fakeRuntime, match func([]string) bool) []string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range f.calls() {
			if match(c) {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// An upgrade recreates the container under agents that were mid-conversation.
// It used to hand the entrypoint a COLD `claude`, stranding a transcript sitting
// right there on the persisted state volume — even though the handler registry
// has declared ResumeLaunch ("claude --continue") for exactly this case all
// along, and its doc names "a GRACEFUL, operator-initiated restart" as the
// reason. Only `dejima agent restart --resume` ever reached it.
func TestUpgradeResumesPrimaryAgent(t *testing.T) {
	h, f := twoAgentServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/upgrade", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}
	if got := f.lastCreate.Env["DEJIMA_LAUNCH"]; got != "claude --continue" {
		t.Errorf("DEJIMA_LAUNCH = %q, want %q (upgrade must continue the conversation)", got, "claude --continue")
	}
}

// The other half of the same bug: upgradeIsland never called reconcileAgents, so
// only the primary (which the entrypoint launches) came back. Every co-located
// agent was left with no tmux session at all — silently, since the upgrade still
// reported 200. wakeIsland had always reconciled; upgrade simply never did.
func TestUpgradeReconcilesNonPrimaryAgents(t *testing.T) {
	h, f := twoAgentServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/upgrade", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}

	got := waitForExec(f, func(c []string) bool {
		return len(c) >= 5 && c[0] == "tmux" && c[1] == "new-session" && strings.Contains(strings.Join(c, " "), "agent-a2")
	})
	if got == nil {
		t.Fatal("no `tmux new-session` for agent a2 — upgrade left the non-primary agent with no session")
	}
	// The non-primary must make the SAME cold/continue choice as the primary;
	// resuming agent 0 while cold-starting the rest is its own inconsistency.
	if joined := strings.Join(got, " "); !strings.Contains(joined, "claude --continue") {
		t.Errorf("a2 launched without resume: %q", joined)
	}
}

// Guard the blast radius. resetIsland shares createContainerForProject with
// upgrade, and reset means "start over" — a resumed conversation is precisely
// what the operator asked to be rid of. This pins the two apart so a later
// refactor can't quietly flip reset onto the resume path.
func TestResetDoesNotResume(t *testing.T) {
	h, f := twoAgentServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body.String())
	}
	if got := f.lastCreate.Env["DEJIMA_LAUNCH"]; got != "claude" {
		t.Errorf("DEJIMA_LAUNCH = %q, want a cold %q — reset must not continue a conversation", got, "claude")
	}
}
