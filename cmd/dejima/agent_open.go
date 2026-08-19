package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/project"
	"github.com/spf13/cobra"
)

// probeGatewayToken runs the framework's dashboard/token command inside the
// container over the façade (a one-shot ssh exec, same host-key pin as the
// tunnel) and returns its raw output. The caller extracts the token (and an
// optional URL) from it. This is how OpenClaw's pinned console token is read back
// without the operator copying anything.
func probeGatewayToken(ctx context.Context, khArgs []string, sshPort, island, host, dashCmd string) (raw string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append([]string{"-o", "BatchMode=yes"}, khArgs...)
	args = append(args, "-p", sshPort, island+"@"+host, dashCmd)
	out, err := exec.CommandContext(ctx, "ssh", args...).Output()
	raw = string(out)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			raw += string(ee.Stderr) // surface the remote error, not just "exit status N"
		}
	}
	return raw, err
}

// errForwardExited / errForwardTimeout distinguish the two ways the -L forward
// can fail to come up. Both mean "there is no tunnel", but only the first has an
// exit status and ssh diagnostics to classify.
var (
	errForwardExited  = errors.New("ssh exited before the forward was listening")
	errForwardTimeout = errors.New("timed out waiting for the forward to listen")
)

// forwardReadyBudget bounds how long we wait for ssh to bind the local end. The
// bind happens after auth, so this covers a slow tailnet handshake, not a slow
// gateway — the remote side isn't dialled until something connects.
const forwardReadyBudget = 15 * time.Second

// waitForForward blocks until the local end of the `-L` forward accepts a
// connection, the ssh process exits, or the budget runs out.
//
// Everything downstream — the token probe, the browser — is only meaningful if
// the tunnel actually came up, and `sshCmd.Start()` cannot tell us that: it
// forks successfully and auth fails afterwards, so a publickey rejection looks
// identical to a healthy start until Wait() returns. Sleeping a fixed interval
// and hoping (what this used to do) races: lose the race and we hand the user a
// browser pointed at a dead port, plus a token diagnosis for a container we
// never reached.
//
// A successful dial only proves ssh is listening locally, which is exactly the
// question — with ExitOnForwardFailure=yes, ssh has authenticated and accepted
// the forward by the time it binds.
func waitForForward(ctx context.Context, port int, exited <-chan struct{}, budget time.Duration) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(budget)
	for {
		// Exit is checked BEFORE the dial, and beats it. If ssh is gone there is no
		// tunnel, so a port that still answers is someone else's service — exactly
		// what happens when `--port` collides and ssh dies on "Address already in
		// use". Dialling first would call that ready and point the browser at a
		// stranger.
		select {
		case <-exited:
			return errForwardExited
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return errForwardTimeout
		}
		select {
		case <-exited:
			return errForwardExited
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// sshFailureHint turns an ssh-layer failure into the remedy that actually fixes
// it, or "" when the failure isn't ssh's (i.e. the command ran in the container
// and failed on its own terms).
//
// This distinction is the whole point. A façade that refuses our key and a
// gateway with no token pinned both surface as "no token came back", and
// conflating them told operators to run `dejima upgrade <island>` — recreating
// the container and restarting every agent in it — to fix an unenrolled SSH key,
// which it cannot do.
//
// ssh reserves exit 255 for its own errors, so that's the backstop when the text
// doesn't match a known shape; a remote command's own 255 is possible but far
// less likely than the ssh-layer case, and the hint is worded to stay true either
// way.
func sshFailureHint(output string, err error) string {
	if err == nil {
		return ""
	}
	low := strings.ToLower(output)
	switch {
	case strings.Contains(low, "permission denied (publickey"),
		strings.Contains(low, "too many authentication failures"),
		strings.Contains(low, "no supported authentication methods"):
		return "this device isn't enrolled with the SSH façade — run `dejima ssh enroll`, then retry"
	case strings.Contains(low, "host key verification failed"),
		strings.Contains(low, "remote host identification has changed"):
		return "the façade's host key didn't verify — run `dejima doctor`"
	case strings.Contains(low, "connection refused"):
		return "nothing is listening on the façade port — check `dejima ssh info`"
	case strings.Contains(low, "connection timed out"),
		strings.Contains(low, "operation timed out"),
		strings.Contains(low, "no route to host"),
		strings.Contains(low, "network is unreachable"):
		return "couldn't reach the façade host — check that your tailnet is up"
	case strings.Contains(low, "could not resolve hostname"):
		return "couldn't resolve the façade hostname — check `dejima ssh info`"
	}
	if isSSHOwnFailure(err) {
		return "ssh failed before reaching the island — check `dejima ssh info` and `dejima ssh enroll`"
	}
	return ""
}

// isSSHOwnFailure reports whether err is ssh's reserved 255, meaning ssh failed
// rather than the remote command.
func isSSHOwnFailure(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 255
}

// lockedBuffer tees ssh's stderr so we can classify it while it still reaches
// the user's terminal. Locked because the timeout path reads it while ssh is
// still running and writing.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// extractGatewayToken pulls a gateway auth token out of dashboard output or a
// URL. It handles the common shapes — a "token=…" query/param, a "Token: …" or
// "OPENCLAW_GATEWAY_TOKEN=…" line, or a quoted "token":"…" — by finding the word
// "token" and capturing the value that follows past separators. Returns "" if
// none is found (the caller then shows the raw output to copy from).
func extractGatewayToken(s string) string {
	low := strings.ToLower(s)
	for from := 0; ; {
		i := strings.Index(low[from:], "token")
		if i < 0 {
			return ""
		}
		j := from + i + len("token")
		// Skip separators between the label and the value.
		for j < len(s) && strings.ContainsRune("=: \t\"'>&?", rune(s[j])) {
			j++
		}
		// Capture the value up to the next separator/quote.
		k := j
		for k < len(s) && !strings.ContainsRune(" \t\r\n\"'>&", rune(s[k])) {
			k++
		}
		if val := s[j:k]; len(val) >= 8 { // a real token, not a stray "token" word
			return val
		}
		from = from + i + len("token")
	}
}

// parseTokenOutput extracts the gateway token from a DashboardTokenCmd's output.
// The command (e.g. `openclaw config get gateway.auth.token`) normally prints the
// bare token, so prefer the last non-empty single-word line; fall back to the
// labeled forms extractGatewayToken handles ("Token: …", "token=…").
func parseTokenOutput(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if s != "" && !strings.ContainsAny(s, " \t") && len(s) >= 8 {
			return s // a bare token on its own line
		}
	}
	return extractGatewayToken(raw)
}

// managedKnownHostsArgs writes the façade's host public key to a dejima-owned
// known_hosts file and returns ssh options that verify against ONLY that file.
// Because it's rewritten from the daemon's authoritative key (fetched over the
// trusted API) on every call, a rotated host key self-heals, and the user's
// global known_hosts is bypassed — so a stale entry there can't cause
// "REMOTE HOST IDENTIFICATION HAS CHANGED". Returns nil (no opts) when the daemon
// supplied no key (older daemon): ssh falls back to its default behavior.
func managedKnownHostsArgs(host, port, hostKey string) ([]string, error) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".dejima")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "known_hosts")
	// known_hosts hosts a non-default port as "[host]:port"; port 22 is bare.
	hostField := host
	if port != "" && port != "22" {
		hostField = "[" + host + "]:" + port
	}
	if err := os.WriteFile(path, []byte(hostField+" "+hostKey+"\n"), 0o600); err != nil {
		return nil, err
	}
	return []string{"-o", "UserKnownHostsFile=" + path, "-o", "StrictHostKeyChecking=yes"}, nil
}

// newAgentOpenCmd opens an agent's web/control UI by forwarding its in-container
// loopback gateway port to localhost over the SSH-façade, then launching a
// browser. Framework-agnostic: the port comes from the agent type's declared
// GatewayPort (OpenClaw 18789, Letta 8283, Goose 3000); types with no localhost
// UI (GatewayPort 0, e.g. a messaging-only gateway) are rejected with a clear
// message.
func newAgentOpenCmd() *cobra.Command {
	var localPort int
	var printOnly, noOpen bool
	cmd := &cobra.Command{
		Use:   "open <island> [agent-id]",
		Short: "Forward an agent's gateway UI to localhost and open it.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			c, err := client()
			if err != nil {
				return err
			}
			isl, err := c.GetIsland(cmd.Context(), island)
			if err != nil {
				return err
			}
			if len(isl.Agents) == 0 {
				return fmt.Errorf("island %q has no agents", island)
			}
			// Resolve the target agent (explicit id or label — id wins — or the
			// primary). Reuses the shared id/label resolver over the island's agents.
			agentType := ""
			agentID := ""
			if len(args) == 2 {
				agentID, err = project.ResolveAgentRef(isl.Agents, args[1])
				if err != nil {
					return err
				}
				for _, a := range isl.Agents {
					if a.ID == agentID {
						agentType = a.Type
					}
				}
			} else {
				agentID, agentType = isl.Agents[0].ID, isl.Agents[0].Type
			}

			// Find the agent type's gateway port.
			types, err := c.ListAgentTypes(cmd.Context())
			if err != nil {
				return err
			}
			gw := 0
			dashTokenCmd, dashTokenSuffix := "", ""
			for _, t := range types {
				if t.Type == agentType {
					gw = t.GatewayPort
					dashTokenCmd, dashTokenSuffix = t.DashboardTokenCmd, t.DashboardTokenSuffix
				}
			}
			if gw == 0 {
				return fmt.Errorf("agent type %q has no localhost gateway to open "+
					"(it may be CLI- or messaging-only)", agentType)
			}

			host, sshPort, hostKey, enabled, err := resolveSSHEndpoint(cmd.Context())
			if err != nil {
				return err
			}
			if !enabled {
				return fmt.Errorf("%s", sshFacadeSetupSteps())
			}

			if localPort == 0 {
				localPort, err = freeLocalPort()
				if err != nil {
					return err
				}
			}
			url := fmt.Sprintf("http://localhost:%d/", localPort)

			// `ssh -N -L local:127.0.0.1:gw island@host -p sshPort` — the façade
			// dials the forward target inside the island's netns, so the agent's
			// loopback-bound gateway is reachable. Uses the user's own ssh (keys +
			// known_hosts from `dejima ssh enroll`).
			// Pin the façade host key in a dejima-managed known_hosts (from the key
			// the daemon just reported over the trusted API), and point ssh at ONLY
			// that file. This bypasses the user's global known_hosts, so a rotated
			// façade key self-heals instead of failing "REMOTE HOST IDENTIFICATION
			// HAS CHANGED" against a stale pinned entry.
			khArgs, err := managedKnownHostsArgs(host, sshPort, hostKey)
			if err != nil {
				return err
			}

			// Keepalives, because this tunnel's whole job is to be LEFT OPEN. A
			// console is something you open and come back to, so the traffic
			// profile is long idle gaps — exactly what NAT tables and firewalls
			// reap. Without ServerAlive, ssh sends nothing during those gaps, the
			// path is silently dropped, and the browser tab reports the GATEWAY as
			// disconnected. The gateway is fine; the tunnel under it is gone.
			// ServerAliveInterval also bounds how long a dead tunnel masquerades as
			// a live one: 3 × 30s, rather than until the OS notices.
			sshArgs := []string{"-N",
				"-o", "ExitOnForwardFailure=yes",
				"-o", "ServerAliveInterval=30",
				"-o", "ServerAliveCountMax=3",
			}
			sshArgs = append(sshArgs, khArgs...)
			sshArgs = append(sshArgs,
				"-L", fmt.Sprintf("%d:127.0.0.1:%d", localPort, gw),
				"-p", sshPort,
				island+"@"+host,
			)
			if printOnly {
				fmt.Printf("%s\nssh %s\n", url, joinArgs(sshArgs))
			}
			fmt.Printf("forwarding %s/%s gateway → %s  (Ctrl-C to stop)\n", island, agentID, url)

			sshCmd := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
			// Tee ssh's stderr: the user still sees it live, and we keep a copy to
			// turn "Permission denied (publickey)" into the remedy for it.
			sshErr := &lockedBuffer{}
			sshCmd.Stdout, sshCmd.Stdin = os.Stdout, os.Stdin
			sshCmd.Stderr = io.MultiWriter(os.Stderr, sshErr)
			if err := sshCmd.Start(); err != nil {
				return fmt.Errorf("start ssh forward: %w", err)
			}

			// Wait() exactly once, in one place; everything else observes `exited`.
			exited := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = sshCmd.Wait()
				close(exited)
			}()

			// Nothing below is meaningful without a live tunnel, so establish that
			// first and fail with the real reason instead of probing a container we
			// never reached.
			if err := waitForForward(cmd.Context(), localPort, exited, forwardReadyBudget); err != nil {
				// Ctrl-C (or any parent deadline) isn't a forward failure — ssh is
				// being torn down deliberately, so report its own status.
				if cmd.Context().Err() != nil {
					<-exited
					return waitErr
				}
				if errors.Is(err, errForwardTimeout) {
					_ = sshCmd.Process.Kill()
				}
				<-exited
				if hint := sshFailureHint(sshErr.String(), waitErr); hint != "" {
					return fmt.Errorf("couldn't open the forward to %s: %s", island, hint)
				}
				if waitErr != nil {
					return fmt.Errorf("couldn't open the forward to %s: %w", island, waitErr)
				}
				return fmt.Errorf("couldn't open the forward to %s: %w", island, err)
			}

			// A gateway whose console needs an auth token (OpenClaw) can't be reached
			// at the bare root. The handler declares how to read its token; we build
			// the tokenized URL its Control UI reads from the query param, so the
			// browser auto-authenticates — no paste. Core stays generic; the framework
			// specifics (the command, the param) are declarative registry data.
			openTarget := url
			if dashTokenCmd != "" && !printOnly {
				raw, derr := probeGatewayToken(cmd.Context(), khArgs, sshPort, island, host, dashTokenCmd)
				token := parseTokenOutput(raw)
				if token != "" {
					suffix := dashTokenSuffix
					if suffix == "" {
						suffix = "#token={token}"
					}
					openTarget = fmt.Sprintf("http://localhost:%d/", localPort) +
						strings.ReplaceAll(suffix, "{token}", neturl.QueryEscape(token))
					// Safety net: if the console still shows a connect form, these are
					// the values to paste.
					fmt.Println()
					fmt.Println("Opening the console with its auth token applied. If it shows a connect")
					fmt.Println("form instead, paste:")
					fmt.Printf("    WebSocket URL:  ws://localhost:%d\n", localPort)
					fmt.Printf("    Gateway Token:  %s\n", token)
					fmt.Println()
				} else if hint := sshFailureHint(raw, derr); hint != "" {
					// The probe never reached the container, so we know nothing about
					// whether a token is pinned. Say only what's true. Emphatically do
					// NOT offer `dejima upgrade` here: recreating the container restarts
					// every agent in the island and cannot fix an ssh-layer fault.
					fmt.Println()
					fmt.Println("The tunnel is up, but couldn't read this agent's console token.")
					fmt.Printf("  %s\n", hint)
					fmt.Println()
					fmt.Println("  Opening the console without a token — if it shows a connect form,")
					fmt.Println("  fix the above and re-run to have the token filled in automatically.")
					fmt.Println()
				} else {
					// No token yet → this container was created before the token was
					// pinned (it only takes effect when the gateway STARTS with it).
					// Explain, and offer the one-time remedy the operator runs — never
					// restart their island unprompted.
					ver := "an older version"
					if ov, e := c.Overview(cmd.Context()); e == nil && ov.DaemonVersion != "" {
						ver = "v" + strings.TrimPrefix(ov.DaemonVersion, "v")
					}
					fmt.Println()
					fmt.Println("This console needs an auth token, and this agent doesn't have one pinned yet.")
					fmt.Println()
					fmt.Println("  Why: the gateway only uses a token if one was set when it STARTED. dejima")
					fmt.Println("  pins one via the agent's launch, which applies when the container is")
					fmt.Println("  (re)created — this container predates that, so it came up without one.")
					fmt.Println("  (Updating dejima doesn't change an already-running container.)")
					fmt.Println()
					fmt.Printf("  Pin it by recreating this island's container once (restarts its agents):\n")
					fmt.Printf("      dejima upgrade %s\n", island)
					fmt.Println("  or in the TUI: select the island, s → \"Upgrade to the current image\".")
					fmt.Printf("  (daemon reports %s; token pinning needs v0.8.48+.)\n", ver)
					if derr != nil {
						fmt.Printf("  (token probe: %v)\n", derr)
					}
					fmt.Println()
				}
			}
			if !noOpen && !printOnly {
				// The forward is confirmed listening, so no sleep-and-hope: open it.
				go func() { _ = openURL(openTarget) }()
			}
			<-exited
			// The tunnel came up and has now ended. Say so, because the browser
			// tab is still open and pointing at a port nothing is listening on —
			// and every console renders that as ITS OWN disconnection. Without
			// this line the most common outcome (ssh exits 0 on a dropped path)
			// prints nothing at all, and the user is left reading a gateway error
			// for a gateway that never failed.
			//
			// Deliberately not printed when the user stopped it themselves: they
			// know, and telling them their Ctrl-C worked is noise.
			if cmd.Context().Err() == nil {
				fmt.Fprintf(os.Stderr, "\nthe forward to %s/%s has closed — the console in your browser is now pointing at nothing.\n", island, agentID)
				fmt.Fprintf(os.Stderr, "re-run `dejima agent open %s %s` to bring it back.\n", island, agentID)
			}
			return waitErr
		},
	}
	cmd.Flags().IntVar(&localPort, "port", 0, "local port to bind (default: a free port)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the URL + ssh command, don't auto-open a browser")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "hold the tunnel but don't open a browser")
	return cmd
}

// freeLocalPort asks the OS for an unused localhost TCP port.
func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// openURL launches the platform browser at url (best-effort).
func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	default:
		return exec.Command("xdg-open", url).Run()
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
