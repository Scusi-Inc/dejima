package api

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/mailbox"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// hibernatedTwoAgentServer stands up a two-agent island whose container is NOT
// running, which is the state wake-on-message exists for.
func hibernatedTwoAgentServer(t *testing.T) (*Server, *fakeRuntime) {
	t.Helper()
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
	f := &fakeRuntime{status: runtime.StatusExited}
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	// The nudge itself is not under test, and injecting one needs an idle
	// heartbeat this island has never posted. Hold it so a failure here can only
	// mean the SESSION is missing.
	srv.idleFn = func(string, string) bool { return false }
	return srv, f
}

// Wake-on-message starts the container and the entrypoint launches the PRIMARY
// agent alone — so a message addressed to a co-located agent wakes an island in
// which that agent has no tmux session. The nudge is then queued for it anyway:
// flushNudges holds for wakeStuckGrace, calls take() (which DELETES the pending
// entry) and injects into a session that does not exist, dropping the error at
// log.Debug. Mail consumed, delivery failed, nothing above Debug — on the path
// that exists precisely because an agent cannot be expected to poll.
//
// This is the sixth site of the gap 14a7ac1 fixed in three restart paths and
// #445 fixed in reset. It is the ORIGINAL: schedule.go's startIslandIfStopped
// says "this mirrors wakeIslandFor's core" directly above itself, and the fix
// landed on the mirror.
func TestWakeOnMessageReconcilesNonPrimaryAgents(t *testing.T) {
	srv, f := hibernatedTwoAgentServer(t)

	before := f.startCalls
	srv.onMailboxArrival(mailbox.Message{Island: "isl", To: "a2", From: "a1"})

	// Control: this asserts a session is missing, so first prove the island was
	// actually woken. Without this, a wake that never happened looks identical.
	if f.startCalls == before {
		t.Fatal("the island was never started — wake-on-message did not run, so the missing session proves nothing")
	}

	got := waitForExec(f, func(c []string) bool {
		return len(c) >= 5 && c[0] == "tmux" && c[1] == "new-session" && strings.Contains(strings.Join(c, " "), "agent-a2")
	})
	if got == nil {
		t.Fatal("no `tmux new-session` for agent a2 — wake-on-message woke the island for a recipient it never gave a session to")
	}
}
