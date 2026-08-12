package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/creack/pty"

	"github.com/aoos/dejima/internal/hosttmux"
)

// PTYSession is one PTY-backed `docker exec` attached to the in-container tmux
// session. Each connected client gets its own PTYSession; tmux's native multi-
// attach gives them a shared screen.
type PTYSession struct {
	cmd *exec.Cmd
	pty *os.File
}

// TermEnv carries the CLIENT terminal's identity into the container, so the
// in-island tmux can decide what the OUTER terminal actually supports.
//
// Without this the island sees only the DAEMON's TERM (docker exec propagates
// the docker CLI's environment, not the end user's), which is identical for
// every connected client and says nothing about any of them. That is why
// image/tmux.conf's capability lines had drifted to matching `,*:` — there was
// no per-client signal to match on — and why a terminal that genuinely can't
// handle RGB/sync/extkeys was being told to emit them anyway.
//
// Both fields are best-effort and may be empty (automation, a non-TTY client,
// or an older dejima client that doesn't send them); empty means "say nothing",
// which leaves the container's own defaults in place.
type TermEnv struct {
	Term      string // client's $TERM, e.g. "xterm-256color"
	ColorTerm string // client's $COLORTERM, e.g. "truecolor"
}

// dockerEnvArgs renders te as `-e KEY=VALUE` args for `docker exec`, dropping
// anything that fails safeTermValue.
//
// These values originate from a network client, so they are filtered rather
// than trusted. They are passed as discrete argv elements (no shell), so the
// risk is not injection but scope: an unfiltered value could smuggle embedded
// NULs/newlines or a second `KEY=` into the container's environment. The
// character class below is a superset of every real TERM/COLORTERM and a subset
// of what could be abused.
func (te TermEnv) dockerEnvArgs() []string {
	var args []string
	if t := canonicalTERM(te.Term); t != "" {
		args = append(args, "-e", "TERM="+t)
	}
	if safeTermValue(te.ColorTerm) {
		// Not canonicalized: COLORTERM is a free-form capability hint ("truecolor",
		// "24bit") that agent processes read directly. tmux ignores it — the RGB
		// gate is TERM-based — so it never needs to resolve against terminfo.
		args = append(args, "-e", "COLORTERM="+te.ColorTerm)
	}
	return args
}

// baseTerminfo is the set of terminfo names the island image is guaranteed to
// resolve. The image installs ncurses-base only (no ncurses-term), so this is
// deliberately small — see canonicalTERM for why passing anything else is unsafe.
var baseTerminfo = map[string]bool{
	"ansi": true, "dumb": true, "linux": true, "vt100": true, "vt220": true,
	"screen": true, "screen-256color": true,
	"tmux": true, "tmux-256color": true,
	"xterm": true, "xterm-color": true, "xterm-256color": true,
	"rxvt-unicode": true, "rxvt-unicode-256color": true,
}

// canonicalTERM maps a client's TERM to one the island can actually resolve,
// returning "" when there is nothing safe to say.
//
// This guard is not cosmetic. tmux refuses to start against a terminfo entry it
// cannot find — "open terminal failed: missing or unsuitable terminal: X" — and
// on the attach path that surfaces as a session that dies instantly and a client
// that reconnects forever. Modern terminals ship names the base terminfo set has
// never heard of (Ghostty sets xterm-ghostty, kitty sets xterm-kitty, and
// alacritty/wezterm/foot/contour set their own), so forwarding TERM verbatim
// would break attach for exactly the users whose terminals work best.
//
// Anything outside baseTerminfo therefore degrades to xterm-256color: present
// everywhere, 256-color, and — via image/tmux.conf's `*-256color` gate — still
// eligible for RGB/extkeys/sync, which is the right answer for every terminal in
// that bucket. A client reporting bare `xterm` keeps `xterm`, which is what
// correctly excludes it from those features.
func canonicalTERM(term string) string {
	if !safeTermValue(term) {
		return ""
	}
	if baseTerminfo[term] {
		return term
	}
	return "xterm-256color"
}

// safeTermValue reports whether v is a plausible TERM/COLORTERM: non-empty,
// short, and drawn only from the characters terminfo names actually use.
func safeTermValue(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '+':
		default:
			return false
		}
	}
	return true
}

// AttachToTmux starts `docker exec -it <container> tmux attach-session -t <session>`
// against a host PTY and returns the session. Caller should Copy() to bridge
// bytes between the PTY and a client transport (e.g., a websocket), and Close()
// when the client disconnects.
//
// rows/cols, when non-zero, size the PTY at creation. This matters: without an
// initial size the docker exec PTY (and the tmux client that runs in it) come
// up at creack/pty's 80x24 default, the agent renders its TUI at that size,
// and the SIGWINCH that arrives from the client's first resize envelope races
// the agent's initial render. Sizing up-front eliminates the race.
//
// te carries the client's terminal identity; see TermEnv.
func AttachToTmux(ctx context.Context, dockerBin, container, tmuxSession string, rows, cols uint16, te TermEnv) (*PTYSession, error) {
	return ExecPTY(ctx, dockerBin, container,
		[]string{"tmux", "new-session", "-A", "-s", tmuxSession}, rows, cols, te)
}

// ExecPTY starts `docker exec -it <container> <cmd...>` against a host PTY and
// returns the session, sized to rows/cols when both are non-zero. It generalizes
// AttachToTmux for the SSH façade, which bridges an SSH session channel to an
// arbitrary in-container command (a login shell, or `bash -lc <exec>`).
func ExecPTY(ctx context.Context, dockerBin, container string, cmd []string, rows, cols uint16, te TermEnv) (*PTYSession, error) {
	if dockerBin == "" {
		dockerBin = "docker"
	}
	args := append([]string{"exec"}, te.dockerEnvArgs()...)
	args = append(args, "-it", container)
	args = append(args, cmd...)
	return startPTY(exec.CommandContext(ctx, dockerBin, args...), rows, cols)
}

// startPTY starts c against a host PTY, sized to rows/cols when both are non-zero.
func startPTY(c *exec.Cmd, rows, cols uint16) (*PTYSession, error) {
	var (
		ptyFile *os.File
		err     error
	)
	if rows > 0 && cols > 0 {
		ptyFile, err = pty.StartWithSize(c, &pty.Winsize{Rows: rows, Cols: cols})
	} else {
		ptyFile, err = pty.Start(c)
	}
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	return &PTYSession{cmd: c, pty: ptyFile}, nil
}

// HostPTY starts cmd directly on the daemon host against a PTY — the operator
// host-terminal path (no container). Mirrors ExecPTY without the docker wrapper.
// This runs UNCONTAINED on the daemon host; it is gated to operators only and
// must never be reachable by an island token (see internal/api/tokenauth.go).
func HostPTY(ctx context.Context, cmd []string, rows, cols uint16, te TermEnv) (*PTYSession, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("host pty: empty command")
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	// The daemon almost always runs under launchd/systemd with TERM unset (or
	// empty). A PTY child like tmux then dies on attach with "open terminal
	// failed: terminal does not support clear" — it can't resolve a terminfo
	// entry for an empty TERM — which the client reads as a dropped connection
	// and retries forever. Guarantee a sane, universally-available terminal type
	// for the host PTY; an already-set non-empty TERM in the daemon env wins.
	// (The in-container path doesn't need this — `docker exec -it` sets TERM.)
	c.Env = ensureTERM(os.Environ(), te.Term)
	return startPTY(c, rows, cols)
}

// ensureTERM returns env with exactly one TERM entry, guaranteed non-empty.
// Precedence: the CLIENT's TERM (when it passes safeTermValue) beats the
// daemon's own, which beats the xterm-256color fallback. Duplicates are
// collapsed so the child can't inherit a stray empty TERM=.
//
// The client wins because it is the only party that knows what the outer
// terminal is: the daemon's TERM describes whatever launchd/systemd handed the
// service, which is the same for every connected operator and descriptive of
// none of them. ~/.dejima/tmux-host.conf gates its capabilities on this value.
func ensureTERM(env []string, preferred string) []string {
	out := make([]string, 0, len(env)+1)
	term := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			term = strings.TrimPrefix(kv, "TERM=")
			continue // re-added canonically below
		}
		out = append(out, kv)
	}
	if t := canonicalTERM(preferred); t != "" {
		term = t
	}
	if term == "" {
		term = "xterm-256color"
	}
	return append(out, "TERM="+term)
}

// AttachToHostTmux attaches (creating if absent) to a tmux session on the daemon
// host — the operator host-terminal equivalent of AttachToTmux. te carries the
// client's terminal identity; see TermEnv.
func AttachToHostTmux(ctx context.Context, tmuxSession string, rows, cols uint16, te TermEnv) (*PTYSession, error) {
	return HostPTY(ctx, hosttmux.NewSessionArgs("new-session", "-A", "-s", tmuxSession), rows, cols, te)
}

// Wait reaps the underlying `docker exec` and returns its exit code. Call it
// after the PTY hits EOF (the in-container process exited). A signal/abnormal
// exit reports 1. Safe on a nil session.
func (s *PTYSession) Wait() int {
	if s == nil || s.cmd == nil {
		return 0
	}
	return exitCode(s.cmd.Wait())
}

// exitCode extracts a process exit code from an *exec.ExitError, defaulting to 0
// on success and 1 on any non-ExitError failure.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// MaxClientSize returns the largest size (per axis) among the tmux clients
// already attached to the session, querying via a throwaway `docker exec`. It's
// used to size a *sizeless* attach (a client that sent no resize — automation,
// a status poller) so it matches the real interactive client instead of coming
// up at creack/pty's 0x0 default. A 0x0 client, under `window-size latest`,
// becomes the "latest" client and collapses the shared window to tmux's 80x24
// fallback — which is exactly the resize bug. Matching the largest existing
// client makes a sizeless attach harmless (it can't shrink the window) and even
// pulls the window toward the real client's dimensions.
//
// ok is false when there are no attached clients yet (the very first connect)
// or the query fails; callers should then fall back to the PTY default.
func MaxClientSize(ctx context.Context, dockerBin, container, tmuxSession string) (rows, cols uint16, ok bool) {
	if dockerBin == "" {
		dockerBin = "docker"
	}
	out, err := exec.CommandContext(ctx, dockerBin, "exec", container,
		"tmux", "list-clients", "-t", tmuxSession,
		"-F", "#{client_height} #{client_width}").Output()
	if err != nil {
		return 0, 0, false
	}
	return parseMaxClientSize(string(out))
}

// HostMaxClientSize is MaxClientSize for a tmux session on the daemon host.
func HostMaxClientSize(ctx context.Context, tmuxSession string) (rows, cols uint16, ok bool) {
	out, err := exec.CommandContext(ctx, "tmux", "list-clients", "-t", tmuxSession,
		"-F", "#{client_height} #{client_width}").Output()
	if err != nil {
		return 0, 0, false
	}
	return parseMaxClientSize(string(out))
}

// parseMaxClientSize returns the largest height/width across `tmux list-clients`
// output lines ("<height> <width>" per client); ok is false if none parse.
func parseMaxClientSize(out string) (rows, cols uint16, ok bool) {
	var maxH, maxW uint16
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		h, err1 := strconv.ParseUint(f[0], 10, 16)
		w, err2 := strconv.ParseUint(f[1], 10, 16)
		if err1 != nil || err2 != nil {
			continue
		}
		if uint16(h) > maxH {
			maxH = uint16(h)
		}
		if uint16(w) > maxW {
			maxW = uint16(w)
		}
	}
	if maxH == 0 || maxW == 0 {
		return 0, 0, false
	}
	return maxH, maxW, true
}

// Resize tells the PTY about a new window size.
func (s *PTYSession) Resize(rows, cols uint16) error {
	if s == nil || s.pty == nil {
		return nil
	}
	return pty.Setsize(s.pty, &pty.Winsize{Rows: rows, Cols: cols})
}

// Read reads from the PTY (container output → client).
func (s *PTYSession) Read(p []byte) (int, error) {
	return s.pty.Read(p)
}

// Write writes to the PTY (client input → container).
func (s *PTYSession) Write(p []byte) (int, error) {
	return s.pty.Write(p)
}

// Close terminates the underlying docker exec and releases the PTY.
func (s *PTYSession) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	if s.pty != nil {
		if err := s.pty.Close(); err != nil {
			firstErr = err
		}
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	return firstErr
}

var _ io.ReadWriteCloser = (*PTYSession)(nil)
