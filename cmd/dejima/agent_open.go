package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// firstURLIn returns the first http(s) URL token in s, trimmed of trailing
// punctuation, or "".
func firstURLIn(s string) string {
	for _, tok := range strings.Fields(s) {
		// https first so the "http" substring of "https://" doesn't win at a later
		// index; find it WITHIN the token so leading punctuation ("(http://…") is ok.
		for _, scheme := range []string{"https://", "http://"} {
			if i := strings.Index(tok, scheme); i >= 0 {
				return strings.TrimRight(tok[i:], ".,)]}\"'>")
			}
		}
	}
	return ""
}

// localizeURL rewrites raw's scheme/host to the local tunnel (plain http on
// localhost:localPort), preserving the path, query (the auth token), and
// fragment — so a URL the framework built for its own loopback address opens
// through the forward.
func localizeURL(raw string, localPort int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Scheme = "http"
	u.Host = fmt.Sprintf("localhost:%d", localPort)
	return u.String(), nil
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
			dashCmd := ""
			for _, t := range types {
				if t.Type == agentType {
					gw = t.GatewayPort
					dashCmd = t.DashboardCmd
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

			// A token-gated console (OpenClaw) needs the gateway to have STARTED with
			// a pinned token. dejima pins one at launch, but that only applies when the
			// container is (re)created — an agent from before the token fix has none.
			// Detect that and recreate the container once, automatically, via the same
			// path as `dejima upgrade`, so the operator doesn't have to. One-time: the
			// token persists, so later opens skip this.
			if dashCmd != "" && !printOnly {
				if raw, _ := probeGatewayToken(cmd.Context(), khArgs, sshPort, island, host, dashCmd); extractGatewayToken(raw) == "" {
					fmt.Printf("This console has no pinned auth token yet — recreating %q's container once to pin it\n(its agents restart briefly; one-time)…\n", island)
					if _, uerr := c.UpgradeIsland(cmd.Context(), island); uerr != nil {
						fmt.Printf("  couldn't recreate automatically (%v).\n  Run this once:  dejima upgrade %s\n", uerr, island)
					} else {
						// Wait for the recreated gateway to come up and pin its token.
						for i := 0; i < 12; i++ {
							time.Sleep(2 * time.Second)
							if r2, _ := probeGatewayToken(cmd.Context(), khArgs, sshPort, island, host, dashCmd); extractGatewayToken(r2) != "" {
								break
							}
						}
					}
				}
			}

			sshArgs := []string{"-N", "-o", "ExitOnForwardFailure=yes"}
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
			sshCmd.Stdout, sshCmd.Stderr, sshCmd.Stdin = os.Stdout, os.Stderr, os.Stdin
			if err := sshCmd.Start(); err != nil {
				return fmt.Errorf("start ssh forward: %w", err)
			}

			// A gateway whose console needs an auth token (OpenClaw) can't be reached
			// at the bare root — the console loads but the WebSocket can't
			// authenticate. dejima pins a stable token at launch and reads it back
			// here (over the façade), so we can show the operator the exact values to
			// paste into the connect form.
			openTarget := url
			if dashCmd != "" && !printOnly {
				raw, derr := probeGatewayToken(cmd.Context(), khArgs, sshPort, island, host, dashCmd)
				token := extractGatewayToken(raw)
				// If the probe also emitted a full URL, auto-open the localized form.
				if u := firstURLIn(raw); u != "" {
					if loc, e := localizeURL(u, localPort); e == nil {
						openTarget = loc
					}
				}
				fmt.Println()
				if token != "" {
					fmt.Println("In the browser's \"Gateway Dashboard\" connect form, paste:")
					fmt.Printf("    WebSocket URL:  ws://localhost:%d\n", localPort)
					fmt.Printf("    Gateway Token:  %s\n", token)
				} else {
					// Still no token even after the auto-recreate above — something
					// deeper. Give the fallback manual step and the daemon version so a
					// not-yet-updated daemon is obvious.
					ver := "an older version"
					if ov, e := c.Overview(cmd.Context()); e == nil && ov.DaemonVersion != "" {
						ver = "v" + strings.TrimPrefix(ov.DaemonVersion, "v")
					}
					fmt.Println("⚠ Still no pinned console token after recreating the container.")
					fmt.Printf("  Daemon reports %s (the token fix needs v0.8.48+).\n", ver)
					fmt.Printf("  Try once by hand:  dejima upgrade %s\n", island)
					fmt.Println("  If it still fails after that, tell me — it's a deeper issue.")
					if derr != nil {
						fmt.Printf("  (token probe: %v)\n", derr)
					}
				}
				fmt.Println()
			}
			if !noOpen && !printOnly {
				// Give the forward a moment to come up, then open the browser.
				go func() {
					time.Sleep(800 * time.Millisecond)
					_ = openURL(openTarget)
				}()
			}
			return sshCmd.Wait()
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
