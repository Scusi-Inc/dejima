package bridge

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// PTYSession is one PTY-backed `docker exec` attached to the in-container tmux
// session. Each connected client gets its own PTYSession; tmux's native multi-
// attach gives them a shared screen.
type PTYSession struct {
	cmd *exec.Cmd
	pty *os.File
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
func AttachToTmux(ctx context.Context, dockerBin, container, tmuxSession string, rows, cols uint16) (*PTYSession, error) {
	if dockerBin == "" {
		dockerBin = "docker"
	}
	cmd := exec.CommandContext(ctx, dockerBin, "exec", "-it", container,
		"tmux", "new-session", "-A", "-s", tmuxSession)
	var (
		ptyFile *os.File
		err     error
	)
	if rows > 0 && cols > 0 {
		ptyFile, err = pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	} else {
		ptyFile, err = pty.Start(cmd)
	}
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	return &PTYSession{cmd: cmd, pty: ptyFile}, nil
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
