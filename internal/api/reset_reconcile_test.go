package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// resetIsland removes the container AND the home volume, then recreates the
// container. The entrypoint relaunches the PRIMARY agent only — every co-located
// agent is the daemon's job, and reset was the last recreate path that never did
// it. Resetting a running multi-agent island therefore brought agent 0 back and
// left the rest with no tmux session at all, until some unrelated path (a wake,
// an unpanic, adopt on the next daemon start) reconciled them by accident.
//
// This is the same gap upgrade had before a0bd706, whose comment sits 66 lines
// below the function that still had it.
func TestResetReconcilesNonPrimaryAgents(t *testing.T) {
	h, f := twoAgentServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body.String())
	}

	got := waitForExec(f, func(c []string) bool {
		return len(c) >= 5 && c[0] == "tmux" && c[1] == "new-session" && strings.Contains(strings.Join(c, " "), "agent-a2")
	})
	if got == nil {
		t.Fatal("no `tmux new-session` for agent a2 — reset left the non-primary agent with no session")
	}
	// And it must come up COLD. reset deletes the state volume the transcript
	// lives on, so `--continue` here would resume a conversation whose history
	// the same handler just removed. TestResetDoesNotResume pins the primary's
	// launch; this pins the agents the daemon starts itself.
	if joined := strings.Join(got, " "); strings.Contains(joined, "--continue") {
		t.Errorf("a2 resumed after a reset: %q", joined)
	}
}

// The other side of the branch, and the reason it is a branch. A reset of a
// HIBERNATED island recreates the container and immediately stops it again to
// honour the prior desired state — so there is nothing to create sessions in,
// and reconciling would fire `tmux new-session` at a stopped container and log
// failures for every co-located agent. The next wake reconciles for real.
//
// A test that asserts something did NOT happen passes just as well when nothing
// happened at all, so this one first proves its own subject: the handler ran far
// enough to recreate the container and to stop it. Only then is the absence of a
// session meaningful.
func TestResetLeavesHibernatedIslandStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateHibernated,
		Agents: []project.AgentSpec{
			{ID: "a1", Type: "claude-code", Tmux: "dejima", Worktree: "/workspace"},
			{ID: "a2", Type: "claude-code", Tmux: "agent-a2", Worktree: "/workspace/.agents/a2"},
		},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{status: runtime.StatusStopped}
	h := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)).Handler()

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body.String())
	}

	// The controls: the handler reached the recreate, and then honoured the
	// hibernated state. Without these two, the assertion below is vacuous.
	if f.lastCreate.Name == "" {
		t.Fatal("no container was created — the handler did not reach the recreate, so the absence of a session proves nothing")
	}
	if f.stopCalls == 0 {
		t.Fatal("container was never stopped — this island was not left hibernated, so this is not the branch under test")
	}

	if got := waitForExec(f, func(c []string) bool {
		return len(c) >= 5 && c[0] == "tmux" && c[1] == "new-session" && strings.Contains(strings.Join(c, " "), "agent-a2")
	}); got != nil {
		t.Errorf("reset created a session in a container it had just stopped: %q", strings.Join(got, " "))
	}
}
