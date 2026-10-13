package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// #333: whether a start resumed the primary was decided by the container's
// HISTORY, not by whoever started it. DEJIMA_LAUNCH is baked at create and
// `docker start` reuses the container, so an operator's `dejima wake` cold-
// started a never-upgraded island, while an unattended scheduled wake resumed any
// island that had ever been upgraded — the reverse of what each should do.
//
// The launch intent moves the choice to start time. These drive each start path
// against a fake that behaves like a NEW image: it has the marker, and reading
// the mounted intent returns what the daemon wrote to the host file, which is
// what the bind mount does for real.
func intentServer(t *testing.T, baked string, newImage bool) (*Server, *project.Project, *fakeRuntime) {
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
	f.execHook = func(cmd []string) (string, string, int, bool) {
		switch {
		case len(cmd) == 3 && cmd[0] == "test" && cmd[2] == launchIntentMarker:
			if newImage {
				return "", "", 0, true
			}
			return "", "", 1, true
		case len(cmd) == 2 && cmd[0] == "cat" && cmd[1] == launchIntentMountPath+"/"+launchIntentFile:
			b, err := os.ReadFile(hostIntentPath(t, p.Name))
			if err != nil {
				return "", "cat: no such file", 1, true
			}
			return string(b), "", 0, true
		case len(cmd) == 2 && cmd[0] == "printenv" && cmd[1] == "DEJIMA_LAUNCH":
			return baked + "\n", "", 0, true
		}
		return "", "", 0, false
	}
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	return srv, p, f
}

func hostIntentPath(t *testing.T, island string) string {
	t.Helper()
	dir, err := paths.LaunchIntentPath(island)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, launchIntentFile)
}

func readIntent(t *testing.T, island string) string {
	t.Helper()
	b, err := os.ReadFile(hostIntentPath(t, island))
	if err != nil {
		t.Fatalf("no launch intent written for %q: %v", island, err)
	}
	return string(b)
}

func TestOperatorWakeResumesTheWholeIsland(t *testing.T) {
	// A never-upgraded island: baked cold. Before #333 an operator's wake could
	// not resume it at all.
	s, p, f := intentServer(t, "claude", true)
	rr := do(t, s.Handler(), http.MethodPost, "/v1/islands/isl/wake", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("wake: %d %s", rr.Code, rr.Body.String())
	}
	if got := readIntent(t, p.Name); got != "claude --continue" {
		t.Errorf("primary intent = %q, want %q: `dejima wake` is the operator bringing agents back", got, "claude --continue")
	}
	if launch := a2Launch(f); !strings.Contains(launch, "claude --continue") {
		t.Errorf("a2 cold-started beside a primary told to resume: %q", launch)
	}
}

// The other direction, which is the one that changes behaviour for islands that
// exist today: an upgraded container carries "claude --continue" forever, so
// every unattended start resumed it. They must now start cold, primary and
// co-located agents alike.
func TestUnattendedStartsStayColdOnAnUpgradedIsland(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(s *Server, p *project.Project)
	}{
		{"scheduled wake", func(s *Server, p *project.Project) {
			s.startIslandIfStopped(context.Background(), p, time.Now())
		}},
		{"wake on message", func(s *Server, p *project.Project) { s.wakeIslandFor(context.Background(), p.Name) }},
		{"unpanic", func(s *Server, p *project.Project) { s.restartToRunning(context.Background(), p) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, f := intentServer(t, "claude --continue", true)
			tc.run(s, p)
			if got := readIntent(t, p.Name); got != "claude" {
				t.Errorf("primary intent = %q, want a cold %q: nobody chose to resume this conversation", got, "claude")
			}
			if launch := a2Launch(f); strings.Contains(launch, "--continue") {
				t.Errorf("a2 resumed on an unattended start: %q", launch)
			}
		})
	}
}

// An island still on an OLDER image ignores the intent file: its start.sh reads
// only the baked DEJIMA_LAUNCH. The daemon must follow what that entrypoint will
// really do, or the primary and the rest split again. Without the marker check
// this case reads the cold intent the daemon just wrote, cold-starts a2, and
// leaves it beside a primary the old entrypoint resumed.
func TestOlderImageFallsBackToTheBakedLaunch(t *testing.T) {
	s, p, f := intentServer(t, "claude --continue", false)
	s.startIslandIfStopped(context.Background(), p, time.Now())
	if launch := a2Launch(f); !strings.Contains(launch, "claude --continue") {
		t.Errorf("a2 cold-started beside a primary that an older entrypoint resumes from its baked env: %q", launch)
	}
}

// Every container must be created with the mount, and its first start must use
// the same launch the env carries — or a container's first boot and its later
// starts would disagree about what "cold" means.
func TestCreateMountsAndWritesTheIntent(t *testing.T) {
	for _, resume := range []bool{false, true} {
		s, p, f := intentServer(t, "", true)
		if err := s.createContainerForProject(context.Background(), p, "", resume); err != nil {
			t.Fatal(err)
		}
		var mounted bool
		for _, b := range f.lastCreate.BindMounts {
			if b.ContainerPath == launchIntentMountPath {
				mounted = true
				if !b.ReadOnly {
					t.Error("launch intent mounted read-write: the island could choose its own launch")
				}
			}
		}
		if !mounted {
			t.Errorf("resume=%v: no bind mount at %s", resume, launchIntentMountPath)
		}
		if got, want := readIntent(t, p.Name), f.lastCreate.Env["DEJIMA_LAUNCH"]; got != want {
			t.Errorf("resume=%v: intent %q disagrees with the baked DEJIMA_LAUNCH %q", resume, got, want)
		}
	}
}

// The contract spans three files in two languages: Go writes and reads the
// intent, start.sh consumes it, and the Dockerfile ships the marker that says
// start.sh does. A rename on one side alone would silently send every island
// back to its baked launch, so pin all three to the Go constants.
func TestLaunchIntentContractMatchesTheImage(t *testing.T) {
	start, err := os.ReadFile("../../image/start.sh")
	if err != nil {
		t.Fatal(err)
	}
	if want := launchIntentMountPath + "/" + launchIntentFile; !strings.Contains(string(start), `"`+want+`"`) {
		t.Errorf("image/start.sh does not read %s; the daemon writes the intent there", want)
	}
	dockerfile, err := os.ReadFile("../../image/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "touch "+launchIntentMarker) {
		t.Errorf("image/Dockerfile does not create %s; without it the daemon ignores the intent on every island", launchIntentMarker)
	}
}
