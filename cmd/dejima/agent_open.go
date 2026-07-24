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

// fetchDashboardURL runs the framework's dashboard command inside the container
// over the façade (a one-shot ssh exec, same host-key pin as the tunnel), reads
// the authenticated URL it prints, and rewrites its host:port onto the local
// tunnel. This is how OpenClaw's console gets its auth token without the operator
// copying anything.
func fetchDashboardURL(ctx context.Context, khArgs []string, sshPort, island, host, dashCmd string, localPort int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append([]string{"-o", "BatchMode=yes"}, khArgs...)
	args = append(args, "-p", sshPort, island+"@"+host, dashCmd)
	out, err := exec.CommandContext(ctx, "ssh", args...).Output()
	if err != nil {
		return "", err
	}
	raw := firstURLIn(string(out))
	if raw == "" {
		return "", fmt.Errorf("no URL in dashboard output")
	}
	return localizeURL(raw, localPort)
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

			// Frameworks whose dashboard needs an auth token (OpenClaw) can't be
			// reached at the bare gateway root — the console loads but the WebSocket
			// can't authenticate. Ask the framework for its OWN authenticated URL
			// (over the façade, in-container), then localize its host:port onto the
			// tunnel so the browser opens a console that actually connects. Best
			// effort: any hiccup falls back to the root URL with a note.
			openTarget := url
			if dashCmd != "" && !printOnly {
				if durl, derr := fetchDashboardURL(cmd.Context(), khArgs, sshPort, island, host, dashCmd, localPort); derr == nil && durl != "" {
					openTarget = durl
				} else {
					fmt.Printf("note: couldn't fetch the authenticated console URL (%v) — opening the gateway root; "+
						"if it says \"could not connect\", the gateway needs its auth token\n", derr)
				}
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
