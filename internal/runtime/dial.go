package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
)

// Dialing a port INSIDE an island.
//
// A framework gateway binds the container's loopback (openclaw launches with
// `--bind loopback`), which is only reachable from within that container's
// network namespace. The host cannot dial it: not on 127.0.0.1, which is the
// host's own loopback, and not on the container's bridge address, because the
// listener is not bound there. `dejima agent open` gets in by tunnelling through
// the SSH façade; anything running in the daemon has to get in another way.
//
// The mechanism is the one internal/sshfacade already uses for direct-tcpip:
// `docker exec` a bash that opens /dev/tcp to the target and pumps it over
// stdio. No extra binary in the image, and — the part that matters — the
// destination is passed as ARGV, never interpolated into the script text, so a
// caller-supplied host or port cannot become a shell command.

// dialScript opens fd 3 to $1:$2 and bridges it to stdio. Kept byte-identical in
// intent to the façade's: read from the socket in the background, write to it in
// the foreground, and tear the reader down when stdin closes.
const dialScript = `exec 3<>/dev/tcp/"$1"/"$2" || exit 1; cat <&3 & p=$!; cat >&3; kill "$p" 2>/dev/null`

// DialContainerPort opens a connection to host:port inside the named container.
//
// The returned Conn owns a `docker exec` subprocess and kills it on Close.
// Deadlines work (it is a net.Pipe end, not a raw os.Pipe), which http.Transport
// and the websocket client both require.
func (d *Docker) DialContainerPort(ctx context.Context, name, host string, port int) (net.Conn, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	cmd := exec.Command(d.bin(), "exec", "-i", name, "bash", "-c", dialScript,
		"dejima-dial", host, fmt.Sprintf("%d", port))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("dial %s:%d in %s: stdin: %w", host, port, name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dial %s:%d in %s: stdout: %w", host, port, name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dial %s:%d in %s: %w", host, port, name, err)
	}
	// ctx bounds the DIAL, not the connection: an http.Transport pools a conn
	// well past the request whose context triggered it, and a websocket outlives
	// every request context there is. Binding the process to ctx would close live
	// connections the moment their opening request finished.
	if err := ctx.Err(); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	return newExecConn(cmd, stdin, stdout, name, host, port), nil
}

// execConn is a net.Conn backed by a `docker exec` subprocess's stdio.
type execConn struct {
	net.Conn // local end of a net.Pipe pair

	cmd      *exec.Cmd
	addr     execAddr
	reapOnce sync.Once
	reaped   chan struct{}

	mu     sync.Mutex
	closed bool
}

func newExecConn(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader, container, host string, port int) *execConn {
	local, remote := net.Pipe()
	c := &execConn{
		Conn:   local,
		cmd:    cmd,
		addr:   execAddr{container: container, host: host, port: port},
		reaped: make(chan struct{}),
	}
	go func() {
		_, _ = io.Copy(stdin, remote)
		_ = stdin.Close()
	}()
	go func() {
		_, _ = io.Copy(remote, stdout)
		// Reap before unblocking the reader, so a caller that sees EOF is seeing a
		// finished subprocess rather than racing its teardown. Bounded: a child
		// that closes stdout without exiting must not wedge the read side.
		c.drain(2 * time.Second)
		_ = remote.Close()
	}()
	return c
}

func (c *execConn) drain(d time.Duration) {
	go c.reap()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c.reaped:
	case <-t.C:
	}
}

// reap waits on the subprocess exactly once. Both the pump and Close call it, so
// it must be idempotent — a second cmd.Wait errors and races the first.
func (c *execConn) reap() {
	c.reapOnce.Do(func() {
		_ = c.cmd.Wait()
		close(c.reaped)
	})
}

// Close tears down the pipe and the subprocess. Killing is right: there is no
// clean shutdown protocol over stdio and the connection is already gone.
func (c *execConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	err := c.Conn.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	// Belt and braces, not the primary reaper. Killing the process ends the
	// stdout pump, which drains and reaps on its way out — so removing this line
	// is a SURVIVABLE mutation and no test pins it. Kept because the redundancy
	// is free and the cost of not reaping in an island is permanent: PID 1 there
	// is `tail -f /dev/null`, which never calls wait().
	go c.reap()
	return err
}

// Reaped closes once the subprocess has been waited on. For tests asserting no
// `docker exec` is left behind — a console session opens and drops many
// connections, and one leaked child per drop would pile up.
func (c *execConn) Reaped() <-chan struct{} { return c.reaped }

func (c *execConn) LocalAddr() net.Addr  { return c.addr }
func (c *execConn) RemoteAddr() net.Addr { return c.addr }

type execAddr struct {
	container string
	host      string
	port      int
}

func (a execAddr) Network() string { return "docker-exec" }
func (a execAddr) String() string  { return fmt.Sprintf("%s!%s:%d", a.container, a.host, a.port) }
