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
		name      string
		in        []string
		preferred string
		want      string
	}{
		{"unset gets a default", []string{"PATH=/bin", "HOME=/root"}, "", "xterm-256color"},
		{"empty TERM gets a default", []string{"TERM=", "PATH=/bin"}, "", "xterm-256color"},
		{"set TERM is preserved", []string{"TERM=screen-256color", "PATH=/bin"}, "", "screen-256color"},
		{"duplicate TERMs collapse to one", []string{"TERM=", "TERM=foo"}, "", "foo"},
		// The client's TERM is the only one that describes the OUTER terminal, so
		// it outranks both the daemon's own and the fallback.
		{"client TERM beats the daemon's", []string{"TERM=screen-256color"}, "xterm", "xterm"},
		{"client TERM beats the fallback", []string{"PATH=/bin"}, "xterm-256color", "xterm-256color"},
		// A client TERM with no terminfo entry is folded, not forwarded.
		{"unresolvable client TERM is folded", []string{"TERM=screen-256color"}, "xterm-ghostty", "xterm-256color"},
		// A hostile or malformed client value must not reach the child's env; it
		// falls back rather than being passed through.
		{"unsafe client TERM is ignored", []string{"TERM=screen-256color"}, "x\nLD_PRELOAD=/evil", "screen-256color"},
		{"empty client TERM is ignored", []string{"TERM=screen-256color"}, "", "screen-256color"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := termOf(ensureTERM(c.in, c.preferred))
			if n != 1 {
				t.Fatalf("want exactly one TERM entry, got %d", n)
			}
			if got != c.want {
				t.Fatalf("TERM = %q, want %q", got, c.want)
			}
		})
	}
}

// TestSafeTermValue guards the filter on client-supplied TERM/COLORTERM. These
// arrive over the network and end up in the container's environment, so the
// rule is allow-list, not deny-list: anything outside the characters terminfo
// names actually use is dropped rather than sanitized.
func TestSafeTermValue(t *testing.T) {
	ok := []string{"xterm-256color", "screen.xterm-new", "wezterm", "tmux-256color", "rxvt-unicode-256color", "truecolor", "24bit"}
	bad := []string{
		"",                        // nothing to say
		"xterm 256color",          // space
		"xterm\nLD_PRELOAD=/evil", // newline smuggling a second entry
		"xterm=foo",               // '=' could split into another var
		"xterm\x00evil",           // embedded NUL
		"$(reboot)",               // shell-looking, even though we never shell out
		strings.Repeat("a", 65),   // over the length cap
	}
	for _, v := range ok {
		if !safeTermValue(v) {
			t.Errorf("safeTermValue(%q) = false, want true", v)
		}
	}
	for _, v := range bad {
		if safeTermValue(v) {
			t.Errorf("safeTermValue(%q) = true, want false", v)
		}
	}
}

// TestCanonicalTERM: the island image ships ncurses-base only, so any TERM it
// cannot resolve must be folded to something it can. tmux refuses to start on an
// unknown terminfo entry ("missing or unsuitable terminal"), which on the attach
// path looks like a session that dies instantly and a client that reconnects
// forever — strictly worse than the rendering issue this whole change is about.
func TestCanonicalTERM(t *testing.T) {
	cases := []struct{ in, want string }{
		// Present in the base set — forwarded verbatim.
		{"xterm", "xterm"},
		{"xterm-256color", "xterm-256color"},
		{"screen-256color", "screen-256color"},
		{"tmux-256color", "tmux-256color"},
		{"linux", "linux"},
		// Real names from terminals with no entry in ncurses-base. Each would
		// break attach if forwarded; each is genuinely 256-color-capable, so
		// xterm-256color both works and keeps it eligible for RGB/extkeys/sync.
		{"xterm-ghostty", "xterm-256color"},
		{"xterm-kitty", "xterm-256color"},
		{"alacritty", "xterm-256color"},
		{"wezterm", "xterm-256color"},
		{"foot-extra", "xterm-256color"},
		// Nothing safe to say.
		{"", ""},
		{"x y", ""},
		{"x\nLD_PRELOAD=/evil", ""},
	}
	for _, c := range cases {
		if got := canonicalTERM(c.in); got != c.want {
			t.Errorf("canonicalTERM(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDockerEnvArgs: the rendered args must be exactly the -e pairs that passed
// the filter, in a form `docker exec` accepts before the container name.
func TestDockerEnvArgs(t *testing.T) {
	cases := []struct {
		name string
		te   TermEnv
		want []string
	}{
		// wezterm has no terminfo entry in the island image, so it is folded to
		// xterm-256color rather than forwarded (which would kill tmux on attach).
		{"unknown terminal is folded", TermEnv{Term: "wezterm", ColorTerm: "truecolor"},
			[]string{"-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor"}},
		{"term only", TermEnv{Term: "xterm-256color"},
			[]string{"-e", "TERM=xterm-256color"}},
		// Bare xterm must survive verbatim: it is what excludes a ConPTY client
		// from RGB/extkeys/sync in image/tmux.conf.
		{"bare xterm is preserved", TermEnv{Term: "xterm"},
			[]string{"-e", "TERM=xterm"}},
		{"empty yields nothing", TermEnv{}, nil},
		{"unsafe values are dropped", TermEnv{Term: "a b", ColorTerm: "x\ny"}, nil},
		{"one bad one good", TermEnv{Term: "a b", ColorTerm: "truecolor"},
			[]string{"-e", "COLORTERM=truecolor"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.te.dockerEnvArgs()
			if len(got) != len(c.want) {
				t.Fatalf("args = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("args = %v, want %v", got, c.want)
				}
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
	s, err := HostPTY(ctx, []string{"sh", "-c", "echo dejima-host-ok TERM=$TERM; sleep 1"}, 24, 80, TermEnv{})
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
	if _, err := HostPTY(context.Background(), nil, 0, 0, TermEnv{}); err == nil {
		t.Error("HostPTY(nil) should error")
	}
}
