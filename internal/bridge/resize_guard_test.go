package bridge

import (
	"os"
	"os/exec"
	"testing"

	"github.com/creack/pty"
)

// A zero dimension must never reach the pty.
//
// startPTY guards `rows > 0 && cols > 0` at creation; Resize did not, though it
// is the one choke point every later resize crosses — the websocket path
// (internal/api/session.go) and the SSH façade, which takes window-change
// dimensions straight off the wire.
//
// A 0x0 pty is never a legitimate request. Under tmux's `window-size latest` the
// resizing client becomes the "latest" one and collapses the shared window,
// which is the black-pane bug in its other form: tmux stays healthy and draws
// its status bar while the pane content is gone.
func TestResizeRefusesZeroDimensions(t *testing.T) {
	c := exec.Command("cat")
	f, err := pty.StartWithSize(c, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		t.Skipf("no pty available here: %v", err)
	}
	t.Cleanup(func() {
		_ = f.Close()
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	})
	sess := &PTYSession{cmd: c, pty: f}

	size := func() *pty.Winsize {
		t.Helper()
		ws, err := pty.GetsizeFull(f)
		if err != nil {
			t.Fatalf("getsize: %v", err)
		}
		return ws
	}
	if got := size(); got.Rows != 50 || got.Cols != 200 {
		t.Fatalf("setup size = %dx%d, want 50x200 — the test proves nothing", got.Rows, got.Cols)
	}

	for _, tc := range []struct {
		name       string
		rows, cols uint16
	}{
		{"both zero", 0, 0},
		{"zero rows", 0, 200},
		{"zero cols", 50, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := sess.Resize(tc.rows, tc.cols); err != nil {
				t.Fatalf("Resize returned %v; it should refuse quietly", err)
			}
			got := size()
			if got.Rows != 50 || got.Cols != 200 {
				t.Errorf("a %dx%d resize was APPLIED (size is now %dx%d) — under "+
					"`window-size latest` that client becomes the latest one and "+
					"collapses the shared tmux window", tc.rows, tc.cols, got.Rows, got.Cols)
			}
		})
	}

	// The guard must not have broken legitimate resizes.
	if err := sess.Resize(24, 80); err != nil {
		t.Fatalf("a real resize failed: %v", err)
	}
	if got := size(); got.Rows != 24 || got.Cols != 80 {
		t.Errorf("a legitimate resize did not apply: %dx%d", got.Rows, got.Cols)
	}
	_ = os.Stdout
}
