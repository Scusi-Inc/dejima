package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// containerResumesPrimary was wired into wakeIsland ONLY. Every other path that
// starts an ALREADY-EXISTING container went on asserting resume=false, so the
// split it was written to prevent stayed live on three of them:
//
//	AdoptExisting            daemon start — a host reboot, `colima start`, a
//	                         `dejima service restart`. The most common restart
//	                         there is, and the one an operator actually hits.
//	restartToRunning         `dejima panic --clear`
//	startIslandIfStopped     a scheduled wake
//
// Observed 2026-09-15: a `colima stop && colima start` bounced nine islands and
// the fleet came back with some agents resumed and some cold. DEJIMA_LAUNCH is
// baked at create time and image/start.sh re-reads it on EVERY start, so an
// island upgraded at any point in its life resumes its primary on all three of
// these paths — while the daemon cold-started every non-primary beside it.
//
// These tests drive the three entry points rather than the reader (which
// resume_consistency_test.go already covers), because the defect was never in
// the reader: it was in who bothered to call it.
func resumePathServer(t *testing.T, baked string) (*Server, *project.Project, *fakeRuntime) {
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
	// StatusExited: the container exists and is stopped — the shape every one of
	// these paths is for. StatusRunning would make startIslandIfStopped return
	// early and would let a regression pass unnoticed.
	f := &fakeRuntime{status: runtime.StatusExited}
	f.execHook = func(cmd []string) (string, string, int, bool) {
		if len(cmd) == 2 && cmd[0] == "printenv" && cmd[1] == "DEJIMA_LAUNCH" {
			return baked + "\n", "", 0, true
		}
		return "", "", 0, false
	}
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	return srv, p, f
}

// a2Launch returns the `tmux new-session` the daemon ran for the NON-primary
// agent — the half of the island the daemon controls, and therefore the half
// that can disagree with the entrypoint.
func a2Launch(f *fakeRuntime) string {
	got := waitForExec(f, func(c []string) bool {
		return len(c) >= 5 && c[0] == "tmux" && c[1] == "new-session" &&
			strings.Contains(strings.Join(c, " "), "agent-a2")
	})
	return strings.Join(got, " ")
}

func TestRestartPathsFollowTheContainersResumeDecision(t *testing.T) {
	// An island that was upgraded at some point carries "claude --continue"
	// permanently. Its entrypoint WILL resume the primary on these paths, so the
	// daemon must bring the rest back the same way.
	t.Run("adopt resumes the rest when the container will resume the primary", func(t *testing.T) {
		s, _, f := resumePathServer(t, "claude --continue")
		s.AdoptExisting(context.Background())
		if launch := a2Launch(f); !strings.Contains(launch, "claude --continue") {
			t.Errorf("a2 cold-started beside a primary the entrypoint resumes: %q", launch)
		}
	})

	t.Run("unpanic resumes the rest when the container will resume the primary", func(t *testing.T) {
		s, p, f := resumePathServer(t, "claude --continue")
		if !s.restartToRunning(context.Background(), p) {
			t.Fatal("restartToRunning reported failure")
		}
		if launch := a2Launch(f); !strings.Contains(launch, "claude --continue") {
			t.Errorf("a2 cold-started beside a primary the entrypoint resumes: %q", launch)
		}
	})

	t.Run("scheduled wake resumes the rest when the container will resume the primary", func(t *testing.T) {
		s, p, f := resumePathServer(t, "claude --continue")
		s.startIslandIfStopped(context.Background(), p, time.Now())
		if launch := a2Launch(f); !strings.Contains(launch, "claude --continue") {
			t.Errorf("a2 cold-started beside a primary the entrypoint resumes: %q", launch)
		}
	})

	// The other direction, which is the one that keeps today's behaviour honest:
	// a never-upgraded island must NOT start resuming conversations the operator
	// did not ask to resume. A fix that simply flipped false to true would pass
	// every case above and fail here.
	t.Run("a cold container stays cold on every path", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			run  func(s *Server, p *project.Project)
		}{
			{"adopt", func(s *Server, p *project.Project) { s.AdoptExisting(context.Background()) }},
			{"unpanic", func(s *Server, p *project.Project) { s.restartToRunning(context.Background(), p) }},
			{"scheduled wake", func(s *Server, p *project.Project) {
				s.startIslandIfStopped(context.Background(), p, time.Now())
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, p, f := resumePathServer(t, "claude")
				tc.run(s, p)
				if launch := a2Launch(f); strings.Contains(launch, "--continue") {
					t.Errorf("a2 resumed a conversation on a cold island: %q", launch)
				}
			})
		}
	})
}
