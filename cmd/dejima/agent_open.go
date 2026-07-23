package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/aoos/dejima/internal/project"
	"github.com/spf13/cobra"
)

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
			for _, t := range types {
				if t.Type == agentType {
					gw = t.GatewayPort
				}
			}
			if gw == 0 {
				return fmt.Errorf("agent type %q has no localhost gateway to open "+
					"(it may be CLI- or messaging-only)", agentType)
			}

			host, sshPort, enabled, err := resolveSSHEndpoint(cmd.Context())
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
			sshArgs := []string{
				"-N", "-o", "ExitOnForwardFailure=yes",
				"-L", fmt.Sprintf("%d:127.0.0.1:%d", localPort, gw),
				"-p", sshPort,
				island + "@" + host,
			}
			if printOnly {
				fmt.Printf("%s\nssh %s\n", url, joinArgs(sshArgs))
			}
			fmt.Printf("forwarding %s/%s gateway → %s  (Ctrl-C to stop)\n", island, agentID, url)

			sshCmd := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
			sshCmd.Stdout, sshCmd.Stderr, sshCmd.Stdin = os.Stdout, os.Stderr, os.Stdin
			if err := sshCmd.Start(); err != nil {
				return fmt.Errorf("start ssh forward: %w", err)
			}
			if !noOpen && !printOnly {
				// Give the forward a moment to come up, then open the browser.
				go func() {
					time.Sleep(800 * time.Millisecond)
					_ = openURL(url)
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
