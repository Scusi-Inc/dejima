package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// An attach must NEVER come up at 0x0.
//
// Under tmux's `window-size latest` (image/tmux.conf) a 0x0 client becomes the
// "latest" client and collapses the SHARED window to tmux's 80x24 fallback. The
// operator's 200x50 terminal then shows a live 80x24 region and blank everywhere
// else, with tmux's own status bar still drawn because tmux was never unhealthy.
// That is the "the terminal went black" report, three times over.
//
// A guard for this already existed and fell through to the bug on its own error
// path: MaxClientSize returns ok=false when the query FAILS as well as when no
// client is attached, and it is itself a `docker exec`, so on a saturated host it
// loses the same race the 500ms handshake just lost.
func TestAttachSizeIsNeverZero(t *testing.T) {
	newSrv := func(t *testing.T, execOK bool) (*Server, *project.Project) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		f := &fakeRuntime{status: runtime.StatusRunning}
		if !execOK {
			f.execHook = func(cmd []string) (string, string, int, bool) {
				return "", "docker: cannot connect", 1, true // every exec fails
			}
		}
		srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
		return srv, &project.Project{Name: "isl", Agents: []project.AgentSpec{{ID: "a1", Tmux: "agent-a1"}}}
	}

	t.Run("the client's own size wins", func(t *testing.T) {
		s, p := newSrv(t, true)
		r, c := s.resolveAttachSize(context.Background(), p, "agent-a1", 40, 160)
		if r != 40 || c != 160 {
			t.Errorf("got %dx%d, want the size the client sent (40x160)", r, c)
		}
	})

	// The case that produced the black screen: no size from the client AND the
	// fallback query fails.
	t.Run("a sizeless attach with a failing query still gets a real size", func(t *testing.T) {
		s, p := newSrv(t, false)
		r, c := s.resolveAttachSize(context.Background(), p, "agent-a1", 0, 0)
		if r == 0 || c == 0 {
			t.Fatalf("got %dx%d — a zero dimension reaches AttachToTmux, which takes "+
				"creack/pty's unsized branch, and the 0x0 client collapses the shared "+
				"tmux window for everyone attached", r, c)
		}
		if r != defaultAttachRows || c != defaultAttachCols {
			t.Errorf("got %dx%d, want the image default %dx%d (image/tmux.conf default-size) "+
				"so the attach lands on the size the session was created at",
				r, c, defaultAttachRows, defaultAttachCols)
		}
	})

	// tmux itself can report a zero-sized client: parseMaxClientSize
	// (internal/bridge/session.go) parses a "0 0" line happily and returns
	// ok=TRUE with zeros. So "the query succeeded" is not the same fact as "the
	// answer is usable", and trusting ok alone puts the zeros straight back.
	t.Run("a query that succeeds with zeros is not a size", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		f := &fakeRuntime{status: runtime.StatusRunning}
		orig := maxClientSizeFn
		maxClientSizeFn = func(context.Context, string, string, string) (uint16, uint16, bool) {
			return 0, 0, true // tmux answered, and the answer is unusable
		}
		t.Cleanup(func() { maxClientSizeFn = orig })
		srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
		p := &project.Project{Name: "isl", Agents: []project.AgentSpec{{ID: "a1", Tmux: "agent-a1"}}}
		r, c := srv.resolveAttachSize(context.Background(), p, "agent-a1", 0, 0)
		if r == 0 || c == 0 {
			t.Errorf("got %dx%d — tmux reported a 0x0 client and we adopted it", r, c)
		}
	})

	// A half-specified size is just as dangerous as a fully zero one: pty.Setsize
	// takes both axes, and tmux resizes on whichever is smaller.
	t.Run("one zero axis is still zero", func(t *testing.T) {
		s, p := newSrv(t, false)
		if r, c := s.resolveAttachSize(context.Background(), p, "agent-a1", 40, 0); r == 0 || c == 0 {
			t.Errorf("got %dx%d — a single zero axis still collapses the window", r, c)
		}
	})
}

// The image default and image/tmux.conf's `default-size` must agree. They are
// the same decision written in two files, and a sizeless attach that does not
// match the size the session was CREATED at resizes the window on arrival —
// which is the thing being fixed, one notch smaller.
func TestAttachDefaultMatchesTheImage(t *testing.T) {
	if defaultAttachRows != 50 || defaultAttachCols != 200 {
		t.Errorf("defaults are %dx%d; image/tmux.conf sets `default-size 200x50`. "+
			"Change the two together.", defaultAttachCols, defaultAttachRows)
	}
}
