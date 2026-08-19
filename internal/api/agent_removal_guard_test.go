package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Removing an agent runs `git worktree remove --force` on its worktree, which
// deletes the directory and everything uncommitted in it. Until this guard,
// nothing checked: `dejima agent rm` had no confirmation, no --force and no
// verification, while purging the whole island REFUSED on the same class of
// work. The careful gate was on the surface a human drives and the ungated one
// on the surface automation drives.
//
// The guard is deliberately narrower than purge's. It blocks on UNCOMMITTED
// work only, because `git worktree remove` does not delete the branch — commits,
// pushed or not, stay reachable in the shared repository. A guard that also
// cried about unpushed commits would be wrong about the loss, and being wrong in
// the alarming direction is how people learn to pass --force without reading.

// dirGitHook answers gitStatusIn's probes per DIRECTORY, so a test can make one
// path dirty and another clean. That is the whole point: a guard that inspected
// /workspace instead of the agent's worktree would look correct, pass a
// single-directory fixture, and check the wrong thing in production.
func dirGitHook(dirtyByDir map[string]int) func([]string) (string, string, int, bool) {
	return func(cmd []string) (string, string, int, bool) {
		joined := strings.Join(cmd, " ")
		dir := ""
		for i, tok := range cmd {
			if tok == "-C" && i+1 < len(cmd) {
				dir = cmd[i+1]
			}
		}
		switch {
		case strings.Contains(joined, "rev-parse --git-dir"):
			return ".git", "", 0, true
		case strings.Contains(joined, "rev-parse --abbrev-ref HEAD"):
			return "agent/w1", "", 0, true
		case strings.Contains(joined, "status --porcelain"):
			n := dirtyByDir[dir]
			lines := make([]string, n)
			for i := range lines {
				lines[i] = " M file"
			}
			return strings.Join(lines, "\n"), "", 0, true
		case strings.Contains(joined, "@{u}..HEAD"), strings.Contains(joined, "HEAD..@{u}"):
			// An agent branch usually has no upstream at all, and even when it does,
			// its commits survive the removal. Report plenty of them: the guard must
			// not block on this, and a fixture of 0 would hide it if it did.
			return "7", "", 0, true
		}
		return "", "", 0, false
	}
}

// saveIslandWithAgent persists an island carrying one removable agent. The
// primary is listed first and is attachable, so the PID-1 rule doesn't fire on
// the agent under test.
func saveIslandWithAgent(t *testing.T, worktree string) {
	t.Helper()
	p := &project.Project{Name: "isl", Agents: []project.AgentSpec{
		{ID: "p1", Type: "claude-code", Worktree: "/workspace"},
		{ID: "w1", Type: "claude-code", Worktree: worktree},
	}}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRemovalWorktreeGuard(t *testing.T) {
	const wt = "/workspace/.agents/w1"
	cases := []struct {
		name       string
		status     runtime.ContainerStatus
		worktree   string
		dirty      map[string]int
		wantBlock  bool
		wantSubstr string
	}{
		{
			name:   "clean worktree removes freely",
			status: runtime.StatusRunning, worktree: wt,
			dirty: map[string]int{},
		},
		{
			name:   "uncommitted work in the worktree blocks",
			status: runtime.StatusRunning, worktree: wt,
			dirty:     map[string]int{wt: 3},
			wantBlock: true, wantSubstr: "3 uncommitted changes",
		},
		{
			// The test that makes the others mean something: the island's workspace
			// is filthy and the agent's worktree is clean. Removing the agent
			// destroys nothing, so it must be allowed. A guard pointed at
			// /workspace would block here and look perfectly reasonable doing it.
			name:   "dirty /workspace does not block a clean agent",
			status: runtime.StatusRunning, worktree: wt,
			dirty: map[string]int{"/workspace": 99},
		},
		{
			// An agent sharing /workspace has no worktree of its own, so
			// removeAgentSession deletes nothing. Nothing to lose, nothing to guard
			// — blocking would make a dirty workspace unremovable-agent territory.
			name:   "agent sharing /workspace is never blocked",
			status: runtime.StatusRunning, worktree: "/workspace",
			dirty: map[string]int{"/workspace": 5},
		},
		{
			name:   "stopped island cannot be verified, so it fails safe",
			status: runtime.StatusStopped, worktree: wt,
			dirty:     map[string]int{wt: 2},
			wantBlock: true, wantSubstr: "not running",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, f := newTestServer(t)
			f.status = tc.status
			f.execHook = dirGitHook(tc.dirty)
			saveIslandWithAgent(t, tc.worktree)

			rr := do(t, h, http.MethodDelete, "/v1/islands/isl/agents/w1", "")
			if !tc.wantBlock {
				if rr.Code != http.StatusNoContent {
					t.Fatalf("removal of a safe agent: got %d, want 204 (body=%s)", rr.Code, rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusConflict {
				t.Fatalf("unforced removal: got %d, want 409 (body=%s)", rr.Code, rr.Body.String())
			}
			if tc.wantSubstr != "" && !strings.Contains(rr.Body.String(), tc.wantSubstr) {
				t.Errorf("409 body = %q, want substring %q", rr.Body.String(), tc.wantSubstr)
			}
			if !strings.Contains(rr.Body.String(), "--force") {
				t.Errorf("409 body = %q, must name the way out", rr.Body.String())
			}
			// A refusal the operator cannot get past is its own failure mode.
			saveIslandWithAgent(t, tc.worktree)
			if rr := do(t, h, http.MethodDelete, "/v1/islands/isl/agents/w1?force=true", ""); rr.Code != http.StatusNoContent {
				t.Fatalf("forced removal: got %d, want 204 (body=%s)", rr.Code, rr.Body.String())
			}
		})
	}
}

// Unpushed commits are NOT a reason to block: `git worktree remove` leaves the
// branch, so they stay reachable. Verified against real git before this guard
// was written — committed work survived, uncommitted did not. Asserting it here
// keeps a later "make it match purge" edit from quietly broadening the guard
// into an alarm nobody believes.
func TestAgentRemovalIgnoresUnpushedCommits(t *testing.T) {
	h, f := newTestServer(t)
	f.status = runtime.StatusRunning
	// dirGitHook reports 7 unpushed commits for every directory and no dirty files.
	f.execHook = dirGitHook(map[string]int{})
	saveIslandWithAgent(t, "/workspace/.agents/w1")

	rr := do(t, h, http.MethodDelete, "/v1/islands/isl/agents/w1", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unpushed commits must not block an agent removal (they survive it): got %d, body=%s",
			rr.Code, rr.Body.String())
	}
}
