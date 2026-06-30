package bridge

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEnsureTERM covers the host-PTY fix: a host tmux child must always see a
// usable, non-empty TERM or it dies on attach with "terminal does not support
// clear". ensureTERM guarantees exactly one, non-empty TERM entry.
func TestEnsureTERM(t *testing.T) {
	termOf := func(env []string) (string, int) {
		val, n := "", 0
		for _, kv := range env {
			if strings.HasPrefix(kv, "TERM=") {
				val = strings.TrimPrefix(kv, "TERM=")
				n++
			}
		}
		return val, n
	}
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"unset gets a default", []string{"PATH=/bin", "HOME=/root"}, "xterm-256color"},
		{"empty TERM gets a default", []string{"TERM=", "PATH=/bin"}, "xterm-256color"},
		{"set TERM is preserved", []string{"TERM=screen-256color", "PATH=/bin"}, "screen-256color"},
		{"duplicate TERMs collapse to one", []string{"TERM=", "TERM=foo"}, "foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := termOf(ensureTERM(c.in))
			if n != 1 {
				t.Fatalf("want exactly one TERM entry, got %d", n)
			}
			if got != c.want {
				t.Fatalf("TERM = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseMaxClientSize(t *testing.T) {
	cases := []struct {
		in     string
		h, w   uint16
		wantOK bool
	}{
		{"24 80", 24, 80, true},
		{"24 80\n50 120\n40 100", 50, 120, true}, // max per axis, independently
		{"", 0, 0, false},
		{"garbage", 0, 0, false},
		{"24 x", 0, 0, false},
		{"  30 90  ", 30, 90, true}, // trimmed
	}
	for _, c := range cases {
		h, w, ok := parseMaxClientSize(c.in)
		if h != c.h || w != c.w || ok != c.wantOK {
			t.Errorf("parseMaxClientSize(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, h, w, ok, c.h, c.w, c.wantOK)
		}
	}
}

// TestHostPTYRunsCommand smoke-tests the host (no-container) PTY path: a command
// runs directly on this host and its output comes back through the PTY.
func TestHostPTYRunsCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// `sleep 1` keeps the pty slave open briefly after the echo so the master can
	// drain the buffered output before the child exits. On macOS, once the last
	// slave closes the master read returns EIO and any un-drained bytes are lost —
	// a bare `echo` exits too fast and the test reads "" (the read loop breaks as
	// soon as it sees the marker, so the sleep adds no real delay).
	// Also assert TERM reaches the child non-empty (the host-tmux fix): even with
	// TERM unset in the parent, the child must see one. Clear it for this process
	// first so the assertion is meaningful regardless of the test runner's env.
	t.Setenv("TERM", "")
	s, err := HostPTY(ctx, []string{"sh", "-c", "echo dejima-host-ok TERM=$TERM; sleep 1"}, 24, 80)
	if err != nil {
		t.Skipf("no PTY available in this environment: %v", err)
	}
	defer s.Close()

	var got strings.Builder
	buf := make([]byte, 256)
	for {
		n, rerr := s.Read(buf)
		if n > 0 {
			got.WriteString(string(buf[:n]))
		}
		if rerr != nil {
			break
		}
		if strings.Contains(got.String(), "dejima-host-ok") {
			break
		}
	}
	if !strings.Contains(got.String(), "dejima-host-ok") {
		t.Errorf("HostPTY output = %q, want it to contain the echoed marker", got.String())
	}
	// The child must NOT see an empty TERM — that's exactly what breaks host tmux.
	if strings.Contains(got.String(), "TERM=\r") || strings.Contains(got.String(), "TERM=\n") ||
		strings.HasSuffix(strings.TrimRight(got.String(), "\r\n"), "TERM=") {
		t.Errorf("HostPTY child saw an empty TERM; output = %q", got.String())
	}
}

func TestHostPTYEmptyCommand(t *testing.T) {
	if _, err := HostPTY(context.Background(), nil, 0, 0); err == nil {
		t.Error("HostPTY(nil) should error")
	}
}
