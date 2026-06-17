package sshfacade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"

	"golang.org/x/crypto/ssh"

	"github.com/aoos/dejima/internal/bridge"
	"github.com/aoos/dejima/internal/project"
)

// Server is the daemon-side SSH front door. It authenticates each connection's
// public key against the target island (the SSH *username* names the island),
// then bridges the session channel into the container with `docker exec` —
// reusing the same brokered-access model as the websocket/PTY path. Islands run
// no sshd; the daemon is the single audited endpoint.
type Server struct {
	signer    ssh.Signer
	dockerBin string
	log       *slog.Logger
}

// NewServer loads (or generates) the daemon host key and returns a ready Server.
func NewServer(log *slog.Logger) (*Server, error) {
	signer, err := HostSigner()
	if err != nil {
		return nil, fmt.Errorf("ssh host key: %w", err)
	}
	return &Server{signer: signer, dockerBin: "docker", log: log}, nil
}

// HostKeyFingerprint returns the SHA256 fingerprint of the daemon's host key, so
// the CLI can print it for clients to pin (known_hosts).
func (s *Server) HostKeyFingerprint() string {
	return ssh.FingerprintSHA256(s.signer.PublicKey())
}

// Serve accepts connections until ln errors or is closed. Each connection is
// handled in its own goroutine.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) serverConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		// The username selects the island; the public key authorizes it against
		// that island's authorized_keys. A bad name, an unknown key, or an island
		// with no registered keys all fail identically (no oracle).
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			island := c.User()
			if err := project.ValidateName(island); err != nil {
				return nil, errAuth
			}
			ok, err := Authorize(island, key)
			if err != nil || !ok {
				return nil, errAuth
			}
			// Carry the authenticated island forward; channel handling reads it
			// from Permissions, never from the (re-suppliable) username again.
			return &ssh.Permissions{Extensions: map[string]string{"island": island}}, nil
		},
	}
	cfg.AddHostKey(s.signer)
	return cfg
}

var errAuth = errors.New("ssh: not authorized for this island")

func (s *Server) handleConn(nConn net.Conn) {
	defer nConn.Close()
	conn, chans, reqs, err := ssh.NewServerConn(nConn, s.serverConfig())
	if err != nil {
		// Failed handshakes (scans, wrong key) are routine; debug-level only.
		s.log.Debug("ssh handshake failed", "remote", nConn.RemoteAddr().String(), "err", err)
		return
	}
	defer conn.Close()
	island := conn.Permissions.Extensions["island"]
	s.log.Info("ssh session opened", "island", island, "remote", nConn.RemoteAddr().String())
	go ssh.DiscardRequests(reqs) // global requests (keepalive etc.) — ignore
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		go s.handleSession(newCh, island)
	}
}

// handleSession services one SSH "session" channel: it collects pty/env/window
// requests, then on shell/exec bridges the channel to a `docker exec` in the
// island and relays the exit status.
func (s *Server) handleSession(newCh ssh.NewChannel, island string) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	p, err := project.Load(island)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "dejima: %v\r\n", err)
		_ = ch.Close()
		return
	}
	container := p.ContainerName()

	var (
		wantPTY bool
		rows    uint16
		cols    uint16
		sess    *bridge.PTYSession // set once an interactive command starts
		started bool
	)
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term                 string
				Cols, Rows, Wpx, Hpx uint32
				Modes                string
			}
			if ssh.Unmarshal(req.Payload, &p) == nil {
				wantPTY, cols, rows = true, uint16(p.Cols), uint16(p.Rows)
			}
			_ = req.Reply(true, nil)
		case "env":
			// Accept but don't forward: the in-container login shell sets its own
			// environment, and forwarding client env into the island is a footgun
			// we don't want by default.
			_ = req.Reply(true, nil)
		case "window-change":
			var p struct{ Cols, Rows, Wpx, Hpx uint32 }
			if ssh.Unmarshal(req.Payload, &p) == nil {
				cols, rows = uint16(p.Cols), uint16(p.Rows)
				if sess != nil {
					_ = sess.Resize(rows, cols)
				}
			}
		case "shell":
			if started {
				_ = req.Reply(false, nil)
				continue
			}
			started = true
			_ = req.Reply(true, nil)
			sess = s.run(ch, container, loginShell(), wantPTY, rows, cols)
		case "exec":
			if started {
				_ = req.Reply(false, nil)
				continue
			}
			started = true
			var p struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &p)
			_ = req.Reply(true, nil)
			sess = s.run(ch, container, execCmd(p.Command), wantPTY, rows, cols)
		case "subsystem":
			if started {
				_ = req.Reply(false, nil)
				continue
			}
			var p struct{ Name string }
			if ssh.Unmarshal(req.Payload, &p) == nil && p.Name == "sftp" {
				// Bridge sftp to the real sftp-server *inside* the container, so
				// file ops run on the island's filesystem as `dejima` — same
				// containment as the shell path. No PTY (sftp is a binary
				// protocol over the channel).
				started = true
				_ = req.Reply(true, nil)
				sess = s.run(ch, container, sftpServerCmd(), false, 0, 0)
			} else {
				// Unknown subsystem — reject so clients fall back gracefully.
				_ = req.Reply(false, nil)
			}
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
	if sess != nil {
		_ = sess.Close()
	}
}

// run bridges the channel to `docker exec` in the container. With a PTY it
// returns the live session (so window-change can resize it); without one it runs
// the command on plain pipes. Either way it relays exit status and closes ch
// when the command finishes.
func (s *Server) run(ch ssh.Channel, container string, cmd []string, wantPTY bool, rows, cols uint16) *bridge.PTYSession {
	ctx := context.Background()
	if wantPTY {
		sess, err := bridge.ExecPTY(ctx, s.dockerBin, container, cmd, rows, cols)
		if err != nil {
			fmt.Fprintf(ch.Stderr(), "dejima: %v\r\n", err)
			sendExit(ch, 1)
			_ = ch.Close()
			return nil
		}
		go func() { _, _ = io.Copy(sess, ch) }() // client → container
		go func() {
			_, _ = io.Copy(ch, sess) // container → client, until the process exits
			sendExit(ch, sess.Wait())
			_ = sess.Close()
			_ = ch.Close()
		}()
		return sess
	}

	// No PTY: a one-shot command (framework backend / VS Code probe). Wire the
	// three standard streams and relay the real exit code.
	args := append([]string{"exec", "-i", container}, cmd...)
	c := exec.CommandContext(ctx, s.dockerBin, args...)
	c.Stdout = ch
	c.Stderr = ch.Stderr()
	// Stdin goes through a pipe WE copy into — never `c.Stdin = ch`. If stdin is
	// not an *os.File, os/exec spawns an internal copier that cmd.Wait() blocks
	// on until stdin hits EOF. But an interactive client (`ssh host cmd`, VS Code
	// Remote-SSH's exec bootstrap) holds its stdin channel open for the whole
	// session, so that EOF never arrives: the command finishes and its output
	// flushes, yet Wait() — and thus exit-status + channel close — would hang
	// forever (the "checking for existing agent host" stall). Owning the copy
	// keeps it off Wait()'s critical path.
	stdin, err := c.StdinPipe()
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "dejima: %v\r\n", err)
		sendExit(ch, 1)
		_ = ch.Close()
		return nil
	}
	if err := c.Start(); err != nil {
		fmt.Fprintf(ch.Stderr(), "dejima: %v\r\n", err)
		sendExit(ch, exitCode(err))
		_ = ch.Close()
		return nil
	}
	go func() {
		_, _ = io.Copy(stdin, ch) // client → container; ends when the client EOFs stdin
		_ = stdin.Close()
	}()
	go func() {
		err := c.Wait()   // returns once the process exits and stdout/stderr drain
		_ = stdin.Close() // stop the stdin copier if it's still pending
		sendExit(ch, exitCode(err))
		_ = ch.Close()
	}()
	return nil
}

// loginShell is the command for an interactive `shell` request — a login shell,
// so a human gets the usual PATH/profile/MOTD they'd expect at a terminal.
func loginShell() []string { return []string{"bash", "-l"} }

// execCmd wraps an `exec` request's command in a NON-login, non-interactive
// shell — matching OpenSSH's `$SHELL -c command`. A login shell would source
// /etc/profile and ~/.bash_profile, whose banners/MOTD/profile.d output pollute
// the channel's stdout; tools that parse exec output (VS Code / Cursor
// Remote-SSH platform detection — "getPlatformForHost") then hang or fail. The
// container image's PATH (docker exec env) is already set, so commands resolve
// without a login shell. A caller needing login semantics can invoke
// `bash -lc …` explicitly in its command.
func execCmd(command string) []string { return []string{"bash", "-c", command} }

// sftpServerCmd is the in-container sftp-server (Debian/Ubuntu path, provided by
// the openssh-sftp-server package baked into the island image). Run directly —
// not via a shell — so the binary protocol on the channel isn't disturbed.
func sftpServerCmd() []string { return []string{"/usr/lib/openssh/sftp-server"} }

// sendExit relays the command's exit status to the client per RFC 4254 §6.10.
func sendExit(ch ssh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
}

// exitCode extracts a process exit code, defaulting to 0 on success and 1 on a
// non-ExitError failure.
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
