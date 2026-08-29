package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/aoos/dejima/internal/runtime"

	"github.com/aoos/dejima/internal/project"
)

// After ANY upgrade, the container permanently carries "claude --continue" —
// DEJIMA_LAUNCH is baked at create time and image/start.sh re-reads it on every
// start. So on the next wake the entrypoint resumes the primary whatever the
// caller intended, and a hardcoded resume=false cold-started every other agent:
// the primary-resumes/rest-cold split the upgrade fix was written to prevent,
// reappearing one hibernate/wake cycle later.
//
// The fix asks the CONTAINER what it is about to do, so the two halves cannot
// disagree. These assert on that reader directly rather than on wake's plumbing,
// because it is the decision that has to be right.
func TestResumeDecisionFollowsTheContainerNotTheCaller(t *testing.T) {
	newSrv := func(t *testing.T, launch string) (*Server, *project.Project) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		f := &fakeRuntime{status: runtime.StatusRunning}
		srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
		f.execHook = func(cmd []string) (string, string, int, bool) {
			if len(cmd) == 2 && cmd[0] == "printenv" && cmd[1] == "DEJIMA_LAUNCH" {
				switch launch {
				case "": // printenv fails: the variable is not set at all
					return "", "", 1, true
				case "<empty>": // printenv SUCCEEDS and the value is empty
					return "\n", "", 0, true
				}
				return launch + "\n", "", 0, true
			}
			return "", "", 0, false
		}
		p := &project.Project{
			Name:   "wildfire",
			Agents: []project.AgentSpec{{ID: "w1", Type: "claude-code", Tmux: "agent-w1"}},
		}
		return srv, p
	}

	t.Run("an upgraded container resumes, so the rest must too", func(t *testing.T) {
		s, p := newSrv(t, "claude --continue")
		if !s.containerResumesPrimary(context.Background(), p) {
			t.Error("the container will relaunch the primary with `claude --continue` and " +
				"this reported cold — every non-primary agent would be cold-started " +
				"alongside a resumed primary, which is the inconsistency being fixed")
		}
	})

	t.Run("a cold container stays cold", func(t *testing.T) {
		s, p := newSrv(t, "claude")
		if s.containerResumesPrimary(context.Background(), p) {
			t.Error("reported resume for a container baked with the plain launch — that " +
				"would resume conversations the operator did not ask to resume")
		}
	})

	t.Run("fails closed when the container cannot be asked", func(t *testing.T) {
		s, p := newSrv(t, "")
		if s.containerResumesPrimary(context.Background(), p) {
			t.Error("reported resume when printenv failed — an unreadable container must " +
				"fall back to today's behaviour, not invent one")
		}
	})

	// A handler with NO resume command must never be reported as resuming — even
	// when the comparison would otherwise succeed by both sides being empty.
	//
	// The first version of this case set launch to "claude --continue" and passed
	// with the guard REMOVED, because "claude --continue" != "" either way. It
	// asserted nothing. Driving the empty-vs-empty case is what makes the guard
	// load-bearing: without it, an empty DEJIMA_LAUNCH matches an empty
	// ResumeLaunch and every codex island would be told to resume a conversation
	// its framework cannot resume.
	t.Run("a type with no resume affordance is never resumed", func(t *testing.T) {
		s, p := newSrv(t, "<empty>")
		p.Agents[0].Type = "codex" // no ResumeLaunch in the registry
		if s.containerResumesPrimary(context.Background(), p) {
			t.Error("reported resume for a handler that has no resume command at all — " +
				"an empty baked launch matched an empty resume launch")
		}
	})
}
