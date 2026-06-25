// Command dejima is the Dejima CLI — a thin client of the Dejima API.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/reposrc"
	"github.com/aoos/dejima/internal/selfupdate"
	"github.com/aoos/dejima/internal/service"
	"github.com/aoos/dejima/internal/version"
)

// execLookPath is a small indirection so resolveDaemonBinary stays testable.
var execLookPath = exec.LookPath

func base64StdEncode(b []byte) string          { return base64.StdEncoding.EncodeToString(b) }
func base64StdDecode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		// Cobra already printed the error. If it's a can't-reach-the-daemon
		// failure on a machine pointed at a host, offer a one-shot troubleshooter.
		maybeOfferConnectionHelp(err)
		os.Exit(1)
	}
}

// maybeOfferConnectionHelp surfaces help when a command can't reach the daemon.
// For a local-socket target it prints a direct diagnosis of why dejimad isn't up
// and the steps to fix it. For a remote target (DEJIMA_HOST or an active
// profile) it offers a one-time interactive troubleshooting walkthrough, firing
// at most once (a marker file) so a host that's down doesn't nag every command.
func maybeOfferConnectionHelp(err error) {
	if err == nil || !isConnectionError(err) {
		return
	}
	// A client pointed at a remote host gets the interactive tailnet/TCP
	// troubleshooter below. A local socket failure is a different problem —
	// dejimad on *this* machine isn't up — so give a direct, actionable read of
	// why and how to fix it (the steps are the answer, no prompt needed).
	if resolveHost() == "" {
		if term.IsTerminal(int(os.Stderr.Fd())) {
			printLocalDaemonHelp(diagnoseLocalDaemon())
		}
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	if connHelpOffered() {
		return
	}
	_ = markConnHelpOffered()
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "Want help troubleshooting the connection? [Y/n]: ")
	line, _ := stdinReader.ReadString('\n')
	if a := strings.TrimSpace(line); a == "" || strings.EqualFold(a, "y") {
		runConnectionTroubleshooter(context.Background())
	} else {
		fmt.Fprintln(os.Stderr, "OK. Re-run with `dejima doctor` or `dejima onboard` for help anytime.")
	}
}

// isConnectionError reports whether err looks like a failure to reach the daemon
// (the client wraps these as "daemon unreachable: …").
func isConnectionError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "daemon unreachable") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "i/o timeout")
}

func connHelpMarkerPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "conn-help-offered"), nil
}

func connHelpOffered() bool {
	p, err := connHelpMarkerPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func markConnHelpOffered() error {
	p, err := connHelpMarkerPath()
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte("offered\n"), 0o600)
}

// runConnectionTroubleshooter prints a focused set of checks for the common
// "can't reach my Dejima host" failures: wrong/missing DEJIMA_HOST, not on the
// tailnet, or the host's daemon not exposing TCP.
func runConnectionTroubleshooter(ctx context.Context) {
	host := strings.TrimSpace(os.Getenv("DEJIMA_HOST"))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, bold("Connection troubleshooter"))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  Target: %s\n", host)

	// 1. Is Tailscale present and up here? The host accepts only tailnet peers.
	if _, err := exec.LookPath("tailscale"); err != nil {
		fmt.Fprintln(os.Stderr, "  ✗ Tailscale isn't installed here — the host accepts only tailnet peers.")
		fmt.Fprintln(os.Stderr, "      macOS: brew install --cask tailscale   ·   Linux: https://tailscale.com/download")
		fmt.Fprintln(os.Stderr, "      then: tailscale up   (log into the SAME account that owns the host)")
	} else if st := tailscaleStatus(); st.BackendState != "Running" {
		fmt.Fprintf(os.Stderr, "  ✗ Tailscale isn't up here (state: %s) — run: tailscale up\n", st.BackendState)
	} else {
		fmt.Fprintln(os.Stderr, "  ✓ Tailscale is up here")
		if len(st.Peer) == 0 {
			fmt.Fprintln(os.Stderr, "    ⚠ but no peers are visible — is the host on the same Tailscale account?")
		}
	}

	// 2. Re-probe the daemon health with a clear timeout.
	if c, err := clientForHost(host); err == nil {
		hctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if herr := c.Health(hctx); herr != nil {
			fmt.Fprintf(os.Stderr, "  ✗ still can't reach the daemon: %v\n", herr)
			fmt.Fprintln(os.Stderr, "    Likely the host isn't exposing TCP. On the HOST, run:")
			fmt.Fprintln(os.Stderr, "        dejima service install --tcp :7273")
			fmt.Fprintln(os.Stderr, "    Also confirm the address/port are right (default port 7273).")
		} else {
			fmt.Fprintln(os.Stderr, "  ✓ the daemon is reachable now — retry your command")
		}
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  More: dejima doctor   ·   dejima onboard")
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dejima",
		Short: "An island for agents to live on.",
		Long: "Dejima is the contained box AI coding agents live inside, plus the lifecycle " +
			"and access plumbing around it. The CLI is a thin client of the Dejima API.",
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       version.Version,
		// No-args → first-run wizard (if not dismissed) or interactive TUI.
		// Verbs continue to work for scripting.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !onboardingDismissed() {
				continueToTUI, err := firstRunPrompt(cmd.Context())
				if err != nil {
					return err
				}
				if !continueToTUI {
					return nil
				}
			}
			// Refuse to open the TUI when there's no real terminal —
			// piped stdin / cron / `dejima | head` etc.
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				fmt.Fprintln(os.Stderr, "dejima (no args) opens the interactive TUI, which needs a real terminal.")
				fmt.Fprintln(os.Stderr, "Try `dejima ls` for a scriptable view, or `dejima --help` for all verbs.")
				return nil
			}
			return runTUI(cmd.Context())
		},
	}
	registerProfileFlags(cmd)
	cmd.AddCommand(
		newInitCmd(),
		newProfileCmd(),
		newHomeCmd(),
		newConnectCmd(),
		newShellCmd(),
		newLsCmd(),
		newAgentCmd(),
		newMsgCmd(),
		newTermCmd(),
		newStatusCmd(),
		newHibernateCmd(),
		newWakeCmd(),
		newResetCmd(),
		newPurgeCmd(),
		newPanicCmd(),
		newUninstallCmd(),
		newCloneCmd(),
		newEjectCmd(),
		newUpgradeCmd(),
		newExecCmd(),
		newCpCmd(),
		newPasteCmd(),
		newPortCmd(),
		newCapCmd(),
		newLinkCmd(),
		newMCPCmd(),
		newAuditCmd(),
		newActivityCmd(),
		newLogsCmd(),
		newImageCmd(),
		newServiceCmd(),
		newSSHCmd(),
		newWebhookCmd(),
		newAuthCmd(),
		newTokenCmd(),
		newProviderCmd(),
		newLogoutAllCmd(),
		newClientsCmd(),
		newOverviewCmd(),
		newDoctorCmd(),
		newOnboardCmd(),
		newAdoptCmd(),
		newUpdateCmd(),
		newTUICmd(),
		newFeedbackCmd(),
	)
	return cmd
}

// --- logout-all ----------------------------------------------------------

func newLogoutAllCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "logout-all",
		Short: "Drop every active client session across all islands.",
		Long: "Forcibly disconnects every websocket attached to any island's session. " +
			"Useful if you suspect an unfamiliar device is attached, or after losing a device. " +
			"Containers and agent processes keep running; only the client connections are revoked.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Print("Drop every active session on this host? [y/N]: ")
				var confirm string
				_, _ = fmt.Scanln(&confirm)
				if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
					return fmt.Errorf("aborted")
				}
			}
			c, err := client()
			if err != nil {
				return err
			}
			count, err := c.RevokeAllSessions(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("revoked %d session(s)\n", count)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	return cmd
}

// --- clients --------------------------------------------------------------

func newClientsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clients",
		Short: "Show recent attach/detach history across all islands.",
		Long: "Surfaces in-memory history of which clients have attached, when, and to which " +
			"island. History is bounded and lives only in the daemon (lost on restart) — " +
			"not a persistent audit log.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			entries, err := c.ClientHistory(cmd.Context())
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("no client connections recorded since the daemon started")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tCLIENT\tISLAND\tACTION")
			for _, e := range entries {
				when := e.AttachedAt
				action := "attached"
				if !e.DetachedAt.IsZero() {
					when = e.DetachedAt
					action = "detached"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					when.Local().Format("2006-01-02 15:04:05"), e.Label, e.Island, action)
			}
			return tw.Flush()
		},
	}
}

// --- exec -----------------------------------------------------------------

func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "exec <name> -- <cmd> [args...]",
		Short:              "Run a one-shot command inside an island.",
		Args:               cobra.MinimumNArgs(2),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rest := args[0], args[1:]
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.ExecInIsland(cmd.Context(), name, rest)
			if err != nil {
				return err
			}
			if resp.Stdout != "" {
				fmt.Print(resp.Stdout)
			}
			if resp.Stderr != "" {
				fmt.Fprint(os.Stderr, resp.Stderr)
			}
			if resp.ExitCode != 0 {
				os.Exit(resp.ExitCode)
			}
			return nil
		},
	}
}

// --- cp -------------------------------------------------------------------

func newCpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Copy a file in or out of an island.",
		Long: "Either source or destination must take the form <island>:<path>. " +
			"Examples:\n  dejima cp foo:/workspace/README.md ./\n  dejima cp ./patch.diff foo:/intake/",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, dst := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			srcIsland, srcPath, srcIsRemote := splitIslandPath(src)
			dstIsland, dstPath, dstIsRemote := splitIslandPath(dst)
			switch {
			case srcIsRemote && !dstIsRemote:
				rc, err := c.ReadFile(cmd.Context(), srcIsland, srcPath)
				if err != nil {
					return err
				}
				defer rc.Close()
				out, err := os.Create(dst)
				if err != nil {
					return err
				}
				defer out.Close()
				_, err = io.Copy(out, rc)
				return err
			case !srcIsRemote && dstIsRemote:
				in, err := os.Open(src)
				if err != nil {
					return err
				}
				defer in.Close()
				return c.WriteFile(cmd.Context(), dstIsland, dstPath, in)
			default:
				return fmt.Errorf("exactly one of src/dst must be an island path (e.g. foo:/workspace/file)")
			}
		},
	}
}

func splitIslandPath(s string) (island, path string, isRemote bool) {
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return "", s, false
	}
	// Avoid mis-parsing Windows-like paths (C:\...).
	if idx == 1 && len(s) > 2 && (s[2] == '/' || s[2] == '\\') {
		return "", s, false
	}
	return s[:idx], s[idx+1:], true
}

// --- logs -----------------------------------------------------------------

func newLogsCmd() *cobra.Command {
	var follow bool
	var agentID string
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Tail an island's logs (or a headless agent's, with --agent).",
		Long: "Tail an island's container logs. For a co-located headless agent, " +
			"use --agent <id> (or the `<name>/<agent>` shorthand) to tail that " +
			"agent's log file. Interactive agents have no logs — attach instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, agent := splitIslandAgent(args[0])
			if agentID != "" {
				agent = agentID
			}
			c, err := client()
			if err != nil {
				return err
			}
			rc, err := c.StreamLogs(cmd.Context(), name, agent, follow)
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(os.Stdout, rc)
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new output until interrupted")
	cmd.Flags().StringVar(&agentID, "agent", "", "tail a specific headless agent's log")
	return cmd
}

// --- service --------------------------------------------------------------

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install or uninstall dejimad as a host service.",
	}
	var systemSvc bool
	cmd.PersistentFlags().BoolVar(&systemSvc, "system", false,
		"manage dejimad as a system-wide LaunchDaemon (macOS only; loads at boot with no desktop login — use on headless Macs; needs sudo)")
	var notifyURL, notifySecret string
	var skipNotifyPrompt bool
	var tcpAddr string
	var skipTCPPrompt bool
	var tokenTCPAddr, sshAddr, autonomyDial string
	var auditOn, auditReads bool
	var auditHMACKeyFile string
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install dejimad as a launchd (macOS) or systemd-user (Linux) service.",
		Long: "Registers dejimad with the host service manager so it survives reboots. " +
			"Interactively offers to expose the Tailscale-pinned TCP listener (so other " +
			"devices can connect) and to set a notification webhook, unless those are " +
			"provided as flags.",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := serviceMgr(systemSvc)
			if err != nil {
				return err
			}
			bin, err := resolveDaemonBinary()
			if err != nil {
				return err
			}
			// Interactive remote-access prompt: only with a TTY, no --tcp flag,
			// no explicit opt-out, and Tailscale present (the listener refuses
			// to start without it). Default on — multi-device access is the
			// common case, and the listener accepts only tailnet peer IPs.
			if tcpAddr == "" && !skipTCPPrompt && term.IsTerminal(int(os.Stdin.Fd())) {
				if _, lookErr := exec.LookPath("tailscale"); lookErr == nil {
					fmt.Println("Expose the daemon to other devices on your tailnet? (Recommended — lets")
					fmt.Println("you run `dejima` from your laptop or phone. Only Tailscale peer IPs are")
					fmt.Println("accepted; off-tailnet connections are refused.)")
					fmt.Print("Enable remote access on :7273? [Y/n]: ")
					var input string
					_, _ = fmt.Scanln(&input)
					if t := strings.TrimSpace(input); t == "" || strings.EqualFold(t, "y") {
						tcpAddr = ":7273"
					}
				}
			}
			var svcArgs []string
			if tcpAddr != "" {
				svcArgs = append(svcArgs, "--tcp", tcpAddr)
			}
			// The in-island autonomy path (#8) and the SSH-façade (#9) are only
			// reachable if the service daemon carries these flags — a hand-run
			// dejimad would collide on the unix socket. Bake them into the plist
			// alongside --tcp so the persisted daemon exposes them.
			if tokenTCPAddr != "" {
				svcArgs = append(svcArgs, "--token-tcp", tokenTCPAddr)
			}
			if sshAddr != "" {
				svcArgs = append(svcArgs, "--ssh", sshAddr)
			}
			if autonomyDial != "" {
				svcArgs = append(svcArgs, "--autonomy-dial", autonomyDial)
			}
			// Audit must be baked into the supervised daemon's args — a flag on a
			// hand-run dejimad doesn't reach the launchd/systemd-managed process,
			// so without this the operational audit log can't be enabled in a
			// normal service deployment. --audit-reads / --audit-hmac-key-file are
			// only meaningful alongside --audit.
			if auditOn {
				svcArgs = append(svcArgs, "--audit")
				if auditReads {
					svcArgs = append(svcArgs, "--audit-reads")
				}
				if auditHMACKeyFile != "" {
					svcArgs = append(svcArgs, "--audit-hmac-key-file", auditHMACKeyFile)
				}
			}
			// Interactive notify prompt: only when we have a TTY, no flag, and
			// the user didn't explicitly opt out.
			if notifyURL == "" && !skipNotifyPrompt && term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Println("Set a notification webhook? (Recommended — get a push whenever any device")
				fmt.Println("connects to one of your islands. Awareness without surveillance.)")
				fmt.Println("  Examples:")
				fmt.Println("    https://ntfy.sh/your-private-topic   (free phone push via ntfy app)")
				fmt.Println("    https://hooks.slack.com/...          (Slack incoming webhook)")
				fmt.Print("URL [skip]: ")
				var input string
				_, _ = fmt.Scanln(&input)
				notifyURL = normalizeWebhookURL(strings.TrimSpace(input))

				if notifyURL != "" && notifySecret == "" {
					fmt.Println()
					fmt.Println("HMAC secret (recommended; signs the POST so the receiver can verify it's yours)")
					fmt.Println("  ntfy.sh ignores headers, but Slack/Discord/your own server will use this.")
					fmt.Print("Secret [skip]: ")
					var s string
					_, _ = fmt.Scanln(&s)
					notifySecret = strings.TrimSpace(s)
				}
			}
			if err := mgr.Install(bin, svcArgs); err != nil {
				return err
			}
			fmt.Printf("installed dejimad service (binary: %s)\n", bin)
			// Record install context so the daemon can later update + restart
			// itself (TUI 'U' / dejima update): the source checkout for a source
			// install, and whether it's a system service (restart domain).
			meta := selfupdate.InstallMeta{System: systemSvc}
			if selfupdate.DetectMode() == selfupdate.ModeSource {
				if cwd, werr := os.Getwd(); werr == nil {
					if dir, ferr := selfupdate.FindCheckout(cwd); ferr == nil {
						meta.SourceDir = dir
					}
				}
			}
			if err := selfupdate.SaveInstallMeta(meta); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not record install metadata for self-update: %v\n", err)
			}
			if tcpAddr != "" {
				fmt.Printf("remote access: listening on %s (tailnet peers only)\n", tcpAddr)
				if fqdn := tailnetFQDN(); fqdn != "" {
					fmt.Printf("  other devices: export DEJIMA_HOST=%s%s\n", fqdn, tcpAddr)
				}
			} else {
				fmt.Println("remote access: disabled (local Unix socket only).")
				fmt.Println("  enable later: dejima service install --tcp :7273")
			}
			if tokenTCPAddr != "" {
				fmt.Printf("in-island autonomy (#8): token-TCP on %s\n", tokenTCPAddr)
			}
			if sshAddr != "" {
				fmt.Printf("ssh façade (#9): listening on %s\n", sshAddr)
				maybeAuthorizeAccountKey()
			}
			if notifyURL != "" {
				if err := waitForDaemonAndSubscribe(cmd.Context(), notifyURL, notifySecret); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not auto-subscribe webhook (%v)\n", err)
					fmt.Fprintf(os.Stderr, "  run later: dejima webhook subscribe --url %s\n", notifyURL)
				} else {
					fmt.Printf("subscribed webhook: %s\n", notifyURL)
				}
			} else {
				fmt.Println("no notification webhook configured.")
				fmt.Println("  set one later: dejima webhook subscribe --url <url>")
			}
			return nil
		},
	}
	installCmd.Flags().StringVar(&tcpAddr, "tcp", "", "expose the Tailscale-pinned TCP listener at this addr (e.g. :7273); empty = local socket only")
	installCmd.Flags().BoolVar(&skipTCPPrompt, "no-tcp-prompt", false, "skip the interactive remote-access prompt")
	installCmd.Flags().StringVar(&tokenTCPAddr, "token-tcp", "", "host-internal addr for the in-island autonomy path (#8), e.g. 127.0.0.1:7274; empty disables")
	installCmd.Flags().StringVar(&sshAddr, "ssh", "", "SSH-façade listen addr (#9), e.g. :2222 or a tailnet IP; empty disables")
	installCmd.Flags().StringVar(&autonomyDial, "autonomy-dial", "", "host:port an in-island brain dials to reach --token-tcp (default host.docker.internal:<token-tcp port>)")
	installCmd.Flags().StringVar(&notifyURL, "notify", "", "auto-subscribe this webhook URL after install")
	installCmd.Flags().StringVar(&notifySecret, "notify-secret", "", "HMAC secret for the auto-subscribed webhook")
	installCmd.Flags().BoolVar(&skipNotifyPrompt, "no-notify-prompt", false, "skip the interactive notification prompt")
	installCmd.Flags().BoolVar(&auditOn, "audit", false, "bake --audit into the service: record an operational audit log (API requests + lifecycle) to the hash-chained ledger")
	installCmd.Flags().BoolVar(&auditReads, "audit-reads", false, "with --audit, also record read (GET) requests")
	installCmd.Flags().StringVar(&auditHMACKeyFile, "audit-hmac-key-file", "", "with --audit, key the ledger chain with the HMAC key in this file (set on a FRESH ledger only)")
	cmd.AddCommand(
		installCmd,
		&cobra.Command{
			Use:   "uninstall",
			Short: "Remove the dejimad service.",
			RunE: func(cmd *cobra.Command, args []string) error {
				mgr, err := serviceMgr(systemSvc)
				if err != nil {
					return err
				}
				if err := mgr.Uninstall(); err != nil {
					return err
				}
				fmt.Println("uninstalled dejimad service")
				return nil
			},
		},
		&cobra.Command{
			Use:   "restart",
			Short: "Restart the dejimad service (e.g. after installing a new binary).",
			RunE: func(cmd *cobra.Command, args []string) error {
				mgr, err := serviceMgr(systemSvc)
				if err != nil {
					return err
				}
				if err := mgr.Restart(); err != nil {
					return err
				}
				fmt.Println("restarted dejimad")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the dejimad service is loaded.",
			RunE: func(cmd *cobra.Command, args []string) error {
				mgr, err := serviceMgr(systemSvc)
				if err != nil {
					return err
				}
				s, err := mgr.Status()
				if err != nil {
					return err
				}
				fmt.Println(s)
				// Beyond "is it loaded?", report how it's supervised and whether
				// that survives a reboot — the headless-Mac footgun doctor flags.
				sup := service.Detect()
				if sup.Mode != "unknown" && sup.Summary != "" {
					fmt.Printf("supervision: %s\n", sup.Summary)
					if sup.Concern != "" {
						fmt.Printf("  ⚠ %s\n", sup.Concern)
					}
				}
				return nil
			},
		},
	)
	return cmd
}

// --- webhook --------------------------------------------------------------

func newWebhookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Manage webhook subscriptions for state-change events.",
	}
	var url, secret string
	var eventNames []string
	subscribe := &cobra.Command{
		Use:   "subscribe",
		Short: "Subscribe a URL to receive event POSTs.",
		Long: "Subscribe a URL to receive event POSTs. With no --event, every event is\n" +
			"delivered; pass --event (repeatable) to scope it — e.g. a headless-box health\n" +
			"monitor wants only `--event container.crashed --event daemon.started`.\n" +
			"See `dejima webhook events` for the catalog.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				return fmt.Errorf("--url is required")
			}
			var types []events.Type
			for _, n := range eventNames {
				t := events.Type(strings.TrimSpace(n))
				if !events.KnownType(t) {
					return fmt.Errorf("unknown event type %q (see `dejima webhook events`)", n)
				}
				types = append(types, t)
			}
			c, err := client()
			if err != nil {
				return err
			}
			sub, err := c.SubscribeWebhook(cmd.Context(), url, secret, types)
			if err != nil {
				return err
			}
			scope := "all events"
			if len(sub.Events) > 0 {
				scope = formatEventTypes(sub.Events)
			}
			fmt.Printf("subscribed: %s -> %s (%s)\n", sub.ID, sub.URL, scope)
			return nil
		},
	}
	subscribe.Flags().StringVar(&url, "url", "", "webhook URL (required)")
	subscribe.Flags().StringVar(&secret, "secret", "", "HMAC secret signed into the X-Dejima-Signature header")
	subscribe.Flags().StringArrayVar(&eventNames, "event", nil, "event type to deliver (repeatable); default all (see: dejima webhook events)")

	list := &cobra.Command{
		Use:   "ls",
		Short: "List webhook subscriptions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			subs, err := c.ListWebhooks(cmd.Context())
			if err != nil {
				return err
			}
			if len(subs) == 0 {
				fmt.Println("no webhook subscriptions")
				return nil
			}
			for _, s := range subs {
				scope := "all"
				if len(s.Events) > 0 {
					scope = formatEventTypes(s.Events)
				}
				fmt.Printf("%s\t%s\t%s\n", s.ID, s.URL, scope)
			}
			return nil
		},
	}

	eventsCmd := &cobra.Command{
		Use:   "events",
		Short: "List the event types a webhook can subscribe to.",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, t := range events.KnownTypes() {
				fmt.Println(t)
			}
			return nil
		},
	}

	unsubscribe := &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a webhook subscription.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.UnsubscribeWebhook(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Println("unsubscribed", args[0])
			return nil
		},
	}

	cmd.AddCommand(subscribe, list, eventsCmd, unsubscribe)
	return cmd
}

// formatEventTypes renders a list of event types as a comma-separated string.
func formatEventTypes(types []events.Type) string {
	parts := make([]string, len(types))
	for i, t := range types {
		parts[i] = string(t)
	}
	return strings.Join(parts, ",")
}

// --- hibernate ------------------------------------------------------------

func newHibernateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hibernate <name>",
		Short: "Stop the island's container, preserving its data.",
		Long: "Use hibernate when memory pressure or long uptimes make a restart healthy. " +
			"Workspace and agent on-disk state are preserved; tmux/PTY session is dropped. " +
			"Resume with `dejima wake <name>`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			info, err := c.HibernateIsland(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("hibernated %s (container: %s)\n", info.Name, info.Container)
			return nil
		},
	}
}

// --- wake -----------------------------------------------------------------

func newWakeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wake <name>",
		Short: "Start a hibernated island.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			info, err := c.WakeIsland(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("woke %s (container: %s)\n", info.Name, info.Container)
			return nil
		},
	}
}

// --- reset ----------------------------------------------------------------

func newResetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reset <name>",
		Short: "Clear agent state. Preserves workspace.",
		Long: "Wipes the agent's on-disk state volume (chat history, scratch files) but " +
			"leaves the workspace untouched. The container is recreated; credentials are " +
			"re-mounted on next start. Useful for \"fresh conversation, same code.\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !force {
				fmt.Printf("This will clear agent state for island %q (chat history, scratch files).\n", name)
				fmt.Printf("Workspace will be preserved. Continue? [y/N]: ")
				var confirm string
				_, _ = fmt.Scanln(&confirm)
				if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
					return fmt.Errorf("aborted")
				}
			}
			c, err := client()
			if err != nil {
				return err
			}
			info, err := c.ResetIsland(cmd.Context(), name)
			if err != nil {
				return err
			}
			fmt.Printf("reset %s (container: %s)\n", info.Name, info.Container)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	return cmd
}

func newCloneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone <name> <new-name>",
		Short: "Duplicate an island, credentials and all.",
		Long: "Creates a new island that is a byte-for-byte copy of an existing one: its\n" +
			"workspace (code + git history) and home volume (tool credentials, agent state)\n" +
			"are copied into fresh volumes. Best run when the source is idle, so the copy is\n" +
			"consistent. Host-filesystem Port grants are NOT carried over (the clone starts\n" +
			"deny-all).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			fmt.Printf("cloning %q → %q (copying workspace + home volumes)…\n", args[0], args[1])
			info, err := c.CloneIsland(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("cloned to island %q (container: %s)\n", info.Name, info.Container)
			return nil
		},
	}
	return cmd
}

func client() (*api.Client, error) {
	return clientForHost(resolveHost())
}

// resolveTarget picks the daemon connection target and a human label for it.
// Precedence:
//  1. DEJIMA_HOST env — an explicit override, and the in-island autonomy path
//     (the daemon injects it per-container), so it must win.
//  2. the saved active profile — what the TUI connection switcher persists, so a
//     remote target survives restarts without re-exporting an env var.
//  3. the local Unix socket ("").
//
// This is the single source of truth for "where do we connect": before, every
// call site read DEJIMA_HOST directly, so a saved profile never became the
// persistent default and a dangling active_profile silently broke the client.
// source is one of "env", "profile", or "local" — where the target came from,
// surfaced in the TUI header so a stale DEJIMA_HOST override is visible.
func resolveTarget() (host, label, source string) {
	// An explicit launch flag (`-p NAME` / `--host`) is the most deliberate
	// expression of intent and is *ephemeral* — it overrides the saved profile
	// and even DEJIMA_HOST for this process only, without persisting anything.
	if h, label, ok := ephemeralTarget(); ok {
		return h, label, "flag"
	}
	if h := strings.TrimSpace(os.Getenv("DEJIMA_HOST")); h != "" {
		return h, h, "env"
	}
	cfg, _ := clientcfg.Load()
	if h, ok := cfg.ActiveHost(); ok {
		return h, cfg.ActiveProfile, "profile"
	}
	return "", "local", "local"
}

// resolveHost is resolveTarget reduced to the connection string, for call sites
// that only need it (the CLI client and the local-vs-remote decision).
func resolveHost() string {
	h, _, _ := resolveTarget()
	return h
}

// clientForHost builds an API client for a connection target: the local Unix
// socket when host is empty, otherwise a TCP client to host:port. Shared by the
// env-driven default and the TUI's connection switcher. When DEJIMA_TOKEN is
// set (the in-island autonomy path — the daemon injects it alongside
// DEJIMA_HOST), the TCP client authenticates with it; remote-device (tailnet)
// access leaves it unset and relies on the tailnet-pinned listener.
func clientForHost(host string) (*api.Client, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return api.NewUnixClient()
	}
	// Guard the choke point: a host carrying a control character (e.g. a stray
	// NUL that slipped through an input field) would otherwise be spliced into a
	// request URL and surface far downstream as an opaque
	// `parse "http://\x00host/...": invalid control character in URL`. Reject it
	// here with a message that names the real problem.
	if i := strings.IndexFunc(host, func(r rune) bool { return r < 0x20 || r == 0x7f }); i >= 0 {
		return nil, fmt.Errorf("invalid host %q: contains a control character at position %d", host, i)
	}
	if token := strings.TrimSpace(os.Getenv("DEJIMA_TOKEN")); token != "" {
		return api.NewTCPClientWithToken(host, token)
	}
	return api.NewTCPClient(host)
}

// versionSkew compares the daemon's reported API version against this client's
// compiled-in one and returns a human-readable warning, or "" if aligned. This
// is what turns a silent-degradation bug (old daemon ignoring a new field) into
// a visible "update X" message.
func versionSkew(daemonVersion string, daemonAPI int) string {
	switch {
	case daemonAPI == 0:
		return fmt.Sprintf("daemon predates version reporting (client api v%d) — update the daemon", version.APIVersion)
	case daemonAPI < version.APIVersion:
		return fmt.Sprintf("daemon api v%d is older than this client (v%d) — update the daemon (%s)",
			daemonAPI, version.APIVersion, daemonVersion)
	case daemonAPI > version.APIVersion:
		return fmt.Sprintf("daemon api v%d is newer than this client (v%d) — update this client",
			daemonAPI, version.APIVersion)
	}
	return ""
}

func serviceMgr(system bool) (service.Manager, error) {
	return service.New(system)
}

// tailnetFQDN returns this host's Tailscale DNS name (without trailing dot),
// or "" if Tailscale isn't present or reachable. Used to print a ready-to-copy
// DEJIMA_HOST for other devices after enabling remote access.
func tailnetFQDN() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if json.Unmarshal(out, &status) != nil {
		return ""
	}
	return strings.TrimSuffix(status.Self.DNSName, ".")
}

// normalizeWebhookURL accepts user input that may be a full URL or a bare
// ntfy.sh topic and returns a canonical URL. Empty input passes through.
func normalizeWebhookURL(in string) string {
	if in == "" {
		return ""
	}
	if strings.HasPrefix(in, "http://") || strings.HasPrefix(in, "https://") {
		return in
	}
	// Looks like an ntfy.sh topic shorthand (no scheme, no slashes, plausible chars)?
	if !strings.Contains(in, "/") && !strings.Contains(in, " ") {
		fmt.Fprintf(os.Stderr, "  (treating %q as an ntfy.sh topic → https://ntfy.sh/%s)\n", in, in)
		return "https://ntfy.sh/" + in
	}
	fmt.Fprintf(os.Stderr, "  warning: URL %q doesn't start with http:// or https://; using as-is\n", in)
	return in
}

// waitForDaemonAndSubscribe polls the daemon socket for up to ~5 seconds, then
// subscribes the webhook. Used by `dejima service install --notify`.
func waitForDaemonAndSubscribe(ctx context.Context, url, secret string) error {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := client()
		if err == nil {
			if healthErr := c.Health(ctx); healthErr == nil {
				_, subErr := c.SubscribeWebhook(ctx, url, secret, nil) // install-time: all events
				return subErr
			} else {
				lastErr = healthErr
			}
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for daemon")
	}
	return lastErr
}

// resolveDaemonBinary finds the dejimad binary next to the dejima binary.
// Fallback: $PATH lookup.
func resolveDaemonBinary() (string, error) {
	self, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(self)
		candidate := filepath.Join(dir, "dejimad")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	if p, err := execLookPath("dejimad"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("could not locate the dejimad binary; build it (`make build`) and put it next to dejima or on $PATH")
}

// --- init -----------------------------------------------------------------

func newInitCmd() *cobra.Command {
	var (
		repo       string
		name       string
		agents     []string
		image      string
		cmdStr     string
		memory     string
		cpus       string
		disk       string
		localCopy  bool
		ghIdentity string
		owner      string
		tagPairs   []string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Provision a new island.",
		Long: "Create a contained workspace for an agent. The repo is cloned into a " +
			"persistent volume inside the island; the agent runs in a tmux session " +
			"(claude-code, codex) or directly (headless, with --cmd).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			multi := len(agents) > 1
			if multi && strings.TrimSpace(cmdStr) != "" {
				return fmt.Errorf("--cmd can't be combined with multiple --agent; create, then `dejima agent add --cmd` for headless agents")
			}
			// Single-agent path keeps the existing scalar validation. With one or
			// zero --agent, agent is that value (or "" → server default).
			agent := ""
			if len(agents) == 1 {
				agent = agents[0]
			}
			if !multi && agent == api.AgentHeadless && strings.TrimSpace(cmdStr) == "" {
				return fmt.Errorf("--cmd is required when --agent %s", api.AgentHeadless)
			}
			if !multi && agent != api.AgentHeadless && strings.TrimSpace(cmdStr) != "" {
				return fmt.Errorf("--cmd is only meaningful with --agent %s", api.AgentHeadless)
			}
			// Resolve the repo client-side: a URL clones directly; a local path
			// clones from its origin by default, or seeds a read-only local copy
			// (--local-copy, or when there's no remote) against a local daemon.
			res, err := reposrc.Resolve(repo, resolveHost() == "", localCopy)
			if err != nil {
				return err
			}
			if name == "" {
				name = project.DeriveNameFromRepo(repo)
			}
			c, err := client()
			if err != nil {
				return err
			}
			fmt.Printf("• %s\n", res.Note)
			// Auto-build the default island image when the daemon doesn't have
			// it yet (fresh host). Custom images stay the user's responsibility.
			if image == "" || image == api.DefaultImage {
				if err := ensureIslandImage(cmd.Context(), c); err != nil {
					return err
				}
			}
			// With multiple --agent, seed them via Agents (element 0 is primary).
			var reqAgents []api.AgentSpecRequest
			if multi {
				for _, a := range agents {
					reqAgents = append(reqAgents, api.AgentSpecRequest{Type: a})
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			tags, err := parseTags(tagPairs)
			if err != nil {
				return err
			}
			if owner == "" {
				owner = defaultOwner()
			}
			info, err := c.CreateIsland(ctx, api.CreateIslandRequest{
				Name:           name,
				Repo:           res.Repo,
				SeedPath:       res.SeedPath,
				Agent:          agent,
				Agents:         reqAgents,
				Image:          image,
				Cmd:            cmdStr,
				GitHubIdentity: ghIdentity,
				Owner:          owner,
				Tags:           tags,
				Resources: api.Resources{
					Memory: memory,
					CPUs:   cpus,
					Disk:   disk,
				},
			})
			if err != nil {
				return err
			}
			fmt.Printf("created island %q (container: %s)\n", info.Name, info.Container)
			// Token-authenticated spawn (a Home brain creating a child): surface
			// the child's token so the brain can drive it. Empty — and unprinted —
			// for operator-driven creates, so no secret hits an operator terminal.
			if info.Token != "" {
				fmt.Printf("child token: %s\n", info.Token)
			}
			if multi {
				fmt.Printf("agents:  dejima agent ls %s\n", info.Name)
			}
			if info.Agent == api.AgentHeadless {
				fmt.Printf("logs:    dejima logs %s --follow\n", info.Name)
			} else {
				fmt.Printf("connect: dejima connect %s\n", info.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "git repo URL, or a local path (cloned from its origin by default) (required)")
	cmd.Flags().BoolVar(&localCopy, "local-copy", false, "for a local path: seed from the working copy on disk (captures unpushed commits) instead of cloning from origin; requires a local daemon")
	cmd.Flags().StringVar(&name, "name", "", "island name (default: derived from repo)")
	cmd.Flags().StringArrayVar(&agents, "agent", nil, "agent to run: claude-code (default), codex, or headless (with --cmd); repeat to seed multiple agents")
	cmd.Flags().StringVar(&image, "image", "", "island image (default: dejima/island:latest)")
	cmd.Flags().StringVar(&cmdStr, "cmd", "", `entrypoint command for --agent headless (e.g. "python my_loop.py"); ignored for other agents`)
	cmd.Flags().StringVar(&ghIdentity, "github-identity", "", "daemon GitHub identity to clone/push as (see `dejima auth status`); default: the daemon's default identity")
	cmd.Flags().StringVar(&memory, "memory", "", "memory limit (e.g. 4G); default: unlimited")
	cmd.Flags().StringVar(&cpus, "cpus", "", "CPU limit (e.g. 2.0); default: unlimited")
	cmd.Flags().StringVar(&disk, "disk", "", "disk size (e.g. 20G); default: unlimited")
	cmd.Flags().StringVar(&owner, "owner", "", "creator label for this island (default: <user>@<host>)")
	cmd.Flags().StringArrayVar(&tagPairs, "tag", nil, "free-form key=value label (repeatable), e.g. --tag team=web --tag env=staging")
	return cmd
}

// --- connect --------------------------------------------------------------

func newConnectCmd() *cobra.Command {
	var label, agentID string
	cmd := &cobra.Command{
		Use:   "connect <name>",
		Short: "Attach to an island's session.",
		Long: "Open an interactive PTY into the island via the Dejima API. With no agent, " +
			"you get a contained shell at /workspace (the same place agents run); target an " +
			"agent with --agent <id> or the `<name>/<agent>` shorthand. Multiple clients can " +
			"attach simultaneously (shared tmux). Disconnect with the normal tmux detach key " +
			"(Ctrl-b then d).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, agent := splitIslandAgent(args[0])
			if agentID != "" {
				agent = agentID // explicit flag wins over the shorthand
			}
			if label == "" {
				label = defaultLabel()
			}
			c, err := client()
			if err != nil {
				return err
			}
			info, err := c.GetIsland(cmd.Context(), name)
			if err != nil {
				return err
			}
			if info.Container != "running" {
				return fmt.Errorf("island %q is not running (container: %s); `dejima wake %s` first", name, info.Container, name)
			}
			// Just after create, the entrypoint may still be cloning the repo into
			// /workspace. Don't drop the operator into an empty dir: if a repo is
			// expected, wait (bounded) for the clone to land before attaching.
			if info.Repo != "" {
				waitForWorkspaceReady(cmd.Context(), c, name)
			}
			// No agent named → a contained shell at /workspace (matches the TUI's
			// Enter-on-island and SSH). An explicit agent attaches that agent.
			if agent == "" {
				return runInShellSession(cmd.Context(), c, name, label, false) // bare CLI — no dashboard to summon back to
			}
			return runSession(cmd.Context(), c, name, agent, label)
		},
	}
	cmd.Flags().StringVar(&label, "as", "", "client label shown in presence (default: $HOSTNAME or 'cli')")
	cmd.Flags().StringVar(&agentID, "agent", "", "agent id to attach to (default: a shell at /workspace)")
	return cmd
}

func newShellCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "shell <name>",
		Short: "Open a shell at an island (contained, at /workspace).",
		Long: "Attach an interactive bash shell INSIDE the island's container at /workspace — " +
			"the same place its agents run, so git, installs, and the repo are all right there. " +
			"It's a single shared, resumable tmux session per island (detach with Ctrl-b then d); " +
			"it is not an agent. This is what the dashboard opens when you press Enter on an island.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if label == "" {
				label = defaultLabel()
			}
			c, err := client()
			if err != nil {
				return err
			}
			info, err := c.GetIsland(cmd.Context(), name)
			if err != nil {
				return err
			}
			if info.Container != "running" {
				return fmt.Errorf("island %q is not running (container: %s); `dejima wake %s` first", name, info.Container, name)
			}
			if info.Repo != "" {
				waitForWorkspaceReady(cmd.Context(), c, name)
			}
			return runInShellSession(cmd.Context(), c, name, label, false) // bare CLI — no dashboard to summon back to
		},
	}
	cmd.Flags().StringVar(&label, "as", "", "client label shown in presence (default: $HOSTNAME or 'cli')")
	return cmd
}

// splitIslandAgent parses an "<island>/<agent>" argument into its parts. A bare
// "<island>" returns an empty agent (meaning the primary).
func splitIslandAgent(arg string) (island, agent string) {
	if i := strings.IndexByte(arg, '/'); i >= 0 {
		return arg[:i], arg[i+1:]
	}
	return arg, ""
}

// defaultLabel produces a client label for presence: $HOSTNAME or "cli".
func defaultLabel() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "cli"
}

// waitForWorkspaceReady polls the daemon until the island's repo clone has
// landed in /workspace, or a bounded deadline passes. Best-effort: any API error
// (older daemon without the endpoint, transient failure) just proceeds to
// attach. It prints a one-time notice so a multi-second clone doesn't look like
// a hang. Cancellable via ctx (Ctrl-C).
func waitForWorkspaceReady(ctx context.Context, c *api.Client, name string) {
	deadline := time.Now().Add(2 * time.Minute)
	notified := false
	for {
		ready, err := c.WorkspaceReady(ctx, name)
		if err != nil || ready || time.Now().After(deadline) {
			return
		}
		if !notified {
			fmt.Printf("waiting for workspace to finish provisioning (cloning)…\n")
			notified = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// runSession is the websocket ↔ local-stdio bridge driving `dejima connect`.
// An empty agentID attaches to the island's primary agent.
// setTerminalTitle sets the local terminal tab/window title via an OSC sequence
// (no-op for a non-TTY stdout, or an empty title used to clear on detach). It's
// written to the local terminal — above any inner tmux — so it's the reliable
// "what am I attached to" cue regardless of the container's tmux config. Control
// bytes are stripped so a crafted name can't inject escape sequences.
func setTerminalTitle(title string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	fmt.Fprintf(os.Stdout, "\033]0;%s\007", sanitizeTitle(title))
}

// sanitizeTitle strips control bytes (including ESC and BEL) from a title so a
// crafted island/agent name can't inject its own terminal escape sequences.
func sanitizeTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, title)
}

// sessionTitle builds the tab title for an island/agent attach: "island/agent",
// or just "island" when no specific agent is named.
func sessionTitle(name, agentID string) string {
	if agentID == "" {
		return name
	}
	return name + "/" + agentID
}

func runSession(ctx context.Context, c *api.Client, name, agentID, label string) error {
	return runSessionLoop(ctx, false, sessionTitle(name, agentID), func(ctx context.Context) (*websocket.Conn, error) {
		return c.DialAgentSession(ctx, name, agentID, label)
	})
}

// runSessionSummonable is runSession with the summon chord (Ctrl-\) enabled — used
// when the attach was launched from the TUI, so the chord can return there.
func runSessionSummonable(ctx context.Context, c *api.Client, name, agentID, label string) error {
	return runSessionLoop(ctx, true, sessionTitle(name, agentID), func(ctx context.Context) (*websocket.Conn, error) {
		return c.DialAgentSession(ctx, name, agentID, label)
	})
}

// runTerminalSession attaches the local terminal to a host terminal's session.
// summonable enables the summon chord (true when launched from the TUI).
func runTerminalSession(ctx context.Context, c *api.Client, id, label string, summonable bool) error {
	return runSessionLoop(ctx, summonable, "host: "+id, func(ctx context.Context) (*websocket.Conn, error) {
		return c.DialTerminalSession(ctx, id, label)
	})
}

// runInShellSession attaches the local terminal to an island's in-island shell —
// a contained bash session at /workspace inside the container. summonable=true
// when launched from the TUI so the Ctrl-\ chord returns to the dashboard.
func runInShellSession(ctx context.Context, c *api.Client, name, label string, summonable bool) error {
	return runSessionLoop(ctx, summonable, name+" — shell", func(ctx context.Context) (*websocket.Conn, error) {
		return c.DialIslandShell(ctx, name, label)
	})
}

// sessReason is why a single attached connection ended.
type sessReason int

const (
	sessReconnect    sessReason = iota // abnormal link drop — re-dial and resume
	sessExitClean                      // server closed cleanly (detach / agent exited / server error)
	sessExitStdinEOF                   // local stdin closed (the terminal window went away)
	sessExitCtx                        // the caller's context was cancelled (Ctrl-C)
	sessExitSummon                     // the summon chord was pressed — break out to the dashboard
)

// summonChord is the byte that breaks out of an attached session back into the
// dashboard (with the host-terminal band open). Ctrl-\ (0x1c): tmux's prefix is
// Ctrl-b and shells/vim don't bind it, so it's a safe in-session escape. Only
// honored when the session was launched from the TUI (summonable); a bare
// `dejima connect` forwards it like any other byte.
const summonChord = 0x1c

// errSummonBand signals that an attached session ended because the user pressed
// the summon chord — runTUI re-enters the dashboard instead of exiting.
var errSummonBand = errors.New("summon band")

// splitOnSummon scans a stdin chunk for the summon chord. If present (and the
// session is summonable), it returns the bytes before the chord and summon=true,
// so the caller forwards the prefix and then breaks out. Otherwise summon=false
// and the chunk is forwarded unchanged.
func splitOnSummon(b []byte, summonable bool) (before []byte, summon bool) {
	if !summonable {
		return b, false
	}
	if i := bytes.IndexByte(b, summonChord); i >= 0 {
		return b[:i], true
	}
	return b, false
}

// classifySessionClose decides, from a websocket read error, whether to exit or
// reconnect. A clean server-initiated NormalClosure means we're done — that's a
// Ctrl-b d detach, the agent exiting, or an explicit server error envelope.
// Anything else (a transport error, which CloseStatus reports as -1, going-away,
// or 1006) is an abnormal drop — a daemon restart, the host sleeping/waking, a
// network blip — that we transparently reconnect through, since the island's
// tmux session keeps running and re-attaching resumes it.
func classifySessionClose(err error, ctx context.Context) sessReason {
	if ctx.Err() != nil {
		return sessExitCtx
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		return sessExitClean
	}
	return sessReconnect
}

// runSessionLoop bridges the local terminal to a session websocket, transparently
// reconnecting to the (persistent) in-island tmux session when the link drops —
// so a daemon restart, the host sleeping, or a network blip no longer closes the
// terminal out from under you when you next type. Stdin is read by one long-lived
// goroutine that outlives individual connections; raw mode is entered once.
func runSessionLoop(ctx context.Context, summonable bool, title string, dial func(context.Context) (*websocket.Conn, error)) error {
	// The first dial surfaces real errors (auth, no such island/agent) directly —
	// no reconnect spinner on a connect that was never going to work.
	conn, err := dial(ctx)
	if err != nil {
		return err
	}

	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		hint := "[dejima] attached. Detach: Ctrl-b d (tmux), or just close the terminal. " +
			"Session keeps running; this client auto-reconnects if the link drops."
		if summonable {
			hint += " Summon the dashboard: Ctrl-\\."
		}
		fmt.Fprintln(os.Stderr, hint)
		oldState, rerr := term.MakeRaw(stdinFd)
		if rerr != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return fmt.Errorf("raw mode: %w", rerr)
		}
		defer func() { _ = term.Restore(stdinFd, oldState) }()
		// Title the local tab to what we're attached to. Emitted to the local
		// terminal (not into the websocket), so it sits above any inner tmux and
		// works regardless of the container's tmux config. Cleared on detach.
		setTerminalTitle(title)
		defer setTerminalTitle("")
	}

	// One long-lived stdin reader → channel; it survives reconnects so keystrokes
	// route to whichever connection is current. Closing stdinDone means the local
	// terminal went away (EOF) — a real exit, never a reconnect.
	stdinCh := make(chan []byte, 64)
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				select {
				case stdinCh <- b:
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	for {
		switch runOneSessionConn(ctx, conn, stdinFd, stdinCh, stdinDone, summonable) {
		case sessReconnect:
			// fall through to the reconnect path below
		case sessExitSummon:
			return errSummonBand
		default:
			return nil
		}
		if term.IsTerminal(stdinFd) {
			fmt.Fprint(os.Stderr, "\r\n[dejima] connection lost — reconnecting…\r\n")
		}
		next, rerr := reconnectSession(ctx, dial, stdinDone)
		if rerr != nil {
			// The only error reconnectSession returns is a positive session-gone
			// signal — exit cleanly with a clear note, not a scary code-1.
			if errors.Is(rerr, api.ErrSessionGone) {
				if term.IsTerminal(stdinFd) {
					fmt.Fprint(os.Stderr, "\r\n[dejima] this session is gone — the island was removed. Closing.\r\n")
				}
				return nil
			}
			return rerr
		}
		if next == nil {
			return nil // ctx cancelled or stdin closed while reconnecting
		}
		conn = next
		if term.IsTerminal(stdinFd) {
			fmt.Fprint(os.Stderr, "\r\n[dejima] reconnected.\r\n")
		}
	}
}

// reconnectSession re-dials with capped exponential backoff until it succeeds.
// The tmux session lives on the daemon, so a dropped client link (laptop sleep,
// network/Tailscale blip, daemon restart) is recoverable — we retry as long as
// the user keeps the terminal open rather than discarding a still-valid session
// on a timer. Returns: the new connection on success; (nil, nil) if the caller
// cancels or the local terminal closes; (nil, err wrapping api.ErrSessionGone)
// only when the daemon positively reports the session/island is gone (404/410) —
// the one case that will never recover.
func reconnectSession(ctx context.Context, dial func(context.Context) (*websocket.Conn, error), stdinDone <-chan struct{}) (*websocket.Conn, error) {
	backoff := 250 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-stdinDone:
			return nil, nil
		case <-time.After(backoff):
		}
		conn, err := dial(ctx)
		if err == nil {
			return conn, nil
		}
		// A purged island / gone session won't come back — stop now. Every other
		// failure is transport-down (daemon unreachable); keep retrying.
		if errors.Is(err, api.ErrSessionGone) {
			return nil, err
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// runOneSessionConn pumps one connection until it ends, returning why. The
// websocket is closed on return; stdin/resize are owned by the caller's
// long-lived reader, so a reconnect resumes without re-reading the terminal.
func runOneSessionConn(ctx context.Context, conn *websocket.Conn, stdinFd int, stdinCh <-chan []byte, stdinDone <-chan struct{}, summonable bool) sessReason {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Re-send the size on (re)attach so tmux opens/resumes at the right dimensions.
	if rows, cols, err := terminalSize(stdinFd); err == nil {
		_ = writeEnvelope(connCtx, conn, api.SessionEnvelope{Type: "resize", Rows: rows, Cols: cols})
	}
	watchTerminalResize(connCtx, stdinFd, func(rows, cols uint16) {
		_ = writeEnvelope(connCtx, conn, api.SessionEnvelope{Type: "resize", Rows: rows, Cols: cols})
	})

	readReason := make(chan sessReason, 1)
	go func() {
		for {
			_, data, err := conn.Read(connCtx)
			if err != nil {
				readReason <- classifySessionClose(err, connCtx)
				return
			}
			var env api.SessionEnvelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			switch env.Type {
			case "hello":
				printPresence("attached", env.Attached)
			case "presence":
				printPresence("now attached", env.Attached)
			case "data":
				if raw, derr := base64StdDecode(env.B64); derr == nil {
					_, _ = os.Stdout.Write(raw)
				}
			case "error":
				fmt.Fprintln(os.Stderr, "server error:", env.B64)
				readReason <- sessExitClean // a real server error — don't loop on it
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return sessExitCtx
		case <-stdinDone:
			return sessExitStdinEOF
		case r := <-readReason:
			return r
		case b := <-stdinCh:
			// The summon chord (Ctrl-\) breaks out to the dashboard: forward any
			// keystrokes before it, then end the session with sessExitSummon. The
			// deferred NormalClosure detaches cleanly — the tmux session lives on.
			before, summon := splitOnSummon(b, summonable)
			if len(before) > 0 {
				_ = writeEnvelope(connCtx, conn, api.SessionEnvelope{Type: "data", B64: base64StdEncode(before)})
			}
			if summon {
				return sessExitSummon
			}
		}
	}
}

func writeEnvelope(ctx context.Context, conn *websocket.Conn, env api.SessionEnvelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func terminalSize(fd int) (rows, cols uint16, err error) {
	// On Windows the size lives on the console *output* handle —
	// GetConsoleScreenBufferInfo fails on the stdin handle callers pass here.
	// sizingFd (build-tagged) redirects to stdout on Windows, identity on Unix.
	w, h, err := term.GetSize(sizingFd(fd))
	if err != nil {
		return 0, 0, err
	}
	return uint16(h), uint16(w), nil
}

func printPresence(prefix string, entries []api.PresenceEntry) {
	if len(entries) == 0 {
		return
	}
	others := make([]string, 0, len(entries))
	for _, e := range entries {
		others = append(others, e.Label)
	}
	fmt.Fprintf(os.Stderr, "[dejima] %s: %s\r\n", prefix, strings.Join(others, ", "))
}

// --- ls -------------------------------------------------------------------

func newLsCmd() *cobra.Command {
	var showAgents, group bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List all islands.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			items, err := c.ListIslands(cmd.Context())
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Println("no islands yet — `dejima init --repo <url>` to create one")
				return nil
			}
			// The daemon's version is the reference for the per-island skew note.
			// Best-effort: an older daemon (no overview/version) just yields no note.
			daemonVer := ""
			if o, ovErr := c.Overview(cmd.Context()); ovErr == nil {
				daemonVer = o.DaemonVersion
			}
			// Whether to render the NOTE column at all: only when at least one island
			// has something to say, so the common all-healthy listing stays clean.
			anyNote := false
			for _, i := range items {
				if islandSkewNote(i, daemonVer) != "" {
					anyNote = true
					break
				}
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			writeRow := func(i api.IslandInfo) {
				agentCol := i.Agent
				if len(i.Agents) > 1 {
					agentCol = fmt.Sprintf("%d agents", len(i.Agents))
				}
				if anyNote {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						i.Name, agentCol, shortenRepo(i.Repo), i.State, i.Container, islandSkewNote(i, daemonVer))
				} else {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						i.Name, agentCol, shortenRepo(i.Repo), i.State, i.Container)
				}
				if showAgents && len(i.Agents) > 1 {
					for _, a := range i.Agents {
						label := a.Type
						if a.Label != "" {
							label = a.Label
						}
						fmt.Fprintf(tw, "  └ %s\t%s\t%s\t%s\t\n", a.ID, label, "", a.State)
					}
				}
			}

			if group {
				// Sibling view: islands sharing a repo read as one project with N
				// islands/agents. UI-only grouping over the same rows.
				for gi, g := range groupByRepo(items) {
					if gi > 0 {
						fmt.Fprintln(tw)
					}
					tail := ""
					if anyNote {
						tail = "\t"
					}
					fmt.Fprintf(tw, "%s\t\t\t\t(%s)%s\n", shortenRepo(g.repo), countNoun(len(g.islands), "island"), tail)
					for _, i := range g.islands {
						writeRow(i)
					}
				}
				return tw.Flush()
			}

			header := "NAME\tAGENT\tREPO\tSTATE\tCONTAINER"
			if anyNote {
				header += "\tNOTE"
			}
			fmt.Fprintln(tw, header)
			for _, i := range items {
				writeRow(i)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVarP(&showAgents, "agents", "a", false, "expand each island's agents")
	cmd.Flags().BoolVarP(&group, "group", "g", false, "group islands that share a repo (multi-agent projects read as one)")
	return cmd
}

// daemonVersion fetches the running daemon's reported version (the reference for
// island version-skew), or "" if it can't be determined (older daemon, transient
// error) — in which case skew comparison degrades to a no-op.
func daemonVersion(ctx context.Context, c *api.Client) string {
	if o, err := c.Overview(ctx); err == nil {
		return o.DaemonVersion
	}
	return ""
}

// islandSkewNote is the short `dejima ls` marker for an island that's behind the
// daemon's version or whose heartbeat never fired — the compact form of the
// doctor finding. Empty when the island is level/healthy or provenance is
// unknown. The full remedy (`dejima upgrade <name>`) lives in `dejima doctor` and
// `dejima status`; ls just flags which islands to look at.
func islandSkewNote(i api.IslandInfo, daemonVer string) string {
	stamp := i.UpgradedVersion
	if stamp == "" {
		stamp = i.BuiltVersion
	}
	if version.IsRelease(daemonVer) && version.IsRelease(stamp) && version.Compare(stamp, daemonVer) < 0 {
		return fmt.Sprintf("stale image (%s < %s) — dejima upgrade %s", stamp, daemonVer, i.Name)
	}
	if i.NeverHeardFrom {
		return "no heartbeat — dejima upgrade " + i.Name
	}
	return ""
}

// islandGroup is a set of islands sharing one repo, for the `dejima ls -g` view.
type islandGroup struct {
	repo    string
	islands []api.IslandInfo
}

// groupByRepo groups islands by their repo URL, preserving first-seen repo order
// and the input order within each group. Islands with no repo collect under a
// "(no repo)" group.
func groupByRepo(items []api.IslandInfo) []islandGroup {
	idx := map[string]int{}
	var groups []islandGroup
	for _, it := range items {
		key := it.Repo
		if key == "" {
			key = "(no repo)"
		}
		i, ok := idx[key]
		if !ok {
			i = len(groups)
			idx[key] = i
			groups = append(groups, islandGroup{repo: key})
		}
		groups[i].islands = append(groups[i].islands, it)
	}
	return groups
}

// newAgentCmd groups per-agent management verbs.
func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage the agents within an island.",
	}
	cmd.AddCommand(newAgentLsCmd(), newAgentAddCmd(), newAgentRmCmd(), newAgentConfigCmd(), newAgentTypesCmd(), newAgentOpenCmd())
	return cmd
}

func newAgentConfigCmd() *cobra.Command {
	var provider, model string
	cmd := &cobra.Command{
		Use:   "config <island> <agent-id>",
		Short: "Set an agent's LLM provider/model (for key-requiring frameworks).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" && model == "" {
				return fmt.Errorf("specify --provider and/or --model")
			}
			c, err := client()
			if err != nil {
				return err
			}
			var req api.AgentConfigRequest
			if cmd.Flags().Changed("provider") {
				req.Provider = &provider
			}
			if cmd.Flags().Changed("model") {
				req.Model = &model
			}
			resp, err := c.ConfigureAgent(cmd.Context(), args[0], args[1], req)
			if err != nil {
				return err
			}
			fmt.Printf("agent %s/%s → provider=%q model=%q\n", args[0], args[1], resp.Provider, resp.Model)
			if resp.RestartRequired {
				fmt.Printf("recreate the island to apply: dejima upgrade %s\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "provider credential name (see `dejima provider ls`)")
	cmd.Flags().StringVar(&model, "model", "", "model string, e.g. anthropic/claude-sonnet-4-6")
	return cmd
}

func newAgentTypesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "types",
		Short: "List the built-in agent types and their capabilities.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			types, err := c.ListAgentTypes(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TYPE\tKIND\tNEEDS KEY\tPROVIDERS\tGATEWAY")
			for _, t := range types {
				kind := "headless"
				if t.Interactive {
					kind = "interactive"
				}
				needsKey := ""
				if t.RequiresProviderKey {
					needsKey = "yes"
				}
				gw := ""
				if t.GatewayPort > 0 {
					gw = fmt.Sprintf("%d", t.GatewayPort)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					t.Type, kind, needsKey, strings.Join(t.SupportedProviders, ","), gw)
			}
			return tw.Flush()
		},
	}
}

func newAgentLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <island>",
		Short: "List the agents in an island.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			agents, err := c.ListAgents(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTYPE\tLABEL\tSTATE\tBRANCH\tWORKTREE")
			for _, a := range agents {
				state := a.State
				if a.Error != "" {
					state = "error"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					a.ID, a.Type, a.Label, state, a.Branch, a.Worktree)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			// Print any orchestration errors in full below the table.
			for _, a := range agents {
				if a.Error != "" {
					fmt.Printf("\n  %s: %s\n", a.ID, a.Error)
				}
			}
			return nil
		},
	}
}

func newAgentAddCmd() *cobra.Command {
	var typ, label, provider, model string
	cmd := &cobra.Command{
		Use:   "add <island>",
		Short: "Add an agent to an island.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			a, err := c.AddAgent(cmd.Context(), args[0], api.AgentSpecRequest{
				Type: typ, Label: label, Provider: provider, Model: model,
			})
			if err != nil {
				return err
			}
			fmt.Printf("added agent %s (%s) to %s — attach with `dejima connect %s/%s`\n",
				a.ID, a.Type, args[0], args[0], a.ID)
			if a.AuthState == "missing-provider-auth" {
				fmt.Printf("note: %s has no model key yet — set one with `dejima provider set %s`\n",
					a.ID, a.Provider)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "agent type (default: same as the island's first agent)")
	cmd.Flags().StringVar(&label, "label", "", "optional label for the agent")
	cmd.Flags().StringVar(&provider, "provider", "", "LLM provider for key-requiring agent types")
	cmd.Flags().StringVar(&model, "model", "", "model string, e.g. anthropic/claude-sonnet-4-6")
	return cmd
}

func newAgentRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <island> <agent-id>",
		Short: "Remove an agent from an island (keeps its branch).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RemoveAgent(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("removed agent %s from %s\n", args[1], args[0])
			return nil
		},
	}
}

// --- status ---------------------------------------------------------------

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Detail view of a single island.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			info, err := c.GetIsland(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("name:        %s\n", info.Name)
			fmt.Printf("repo:        %s\n", info.Repo)
			fmt.Printf("agent:       %s\n", info.Agent)
			fmt.Printf("image:       %s\n", info.Image)
			if stamp := info.UpgradedVersion; stamp != "" {
				fmt.Printf("built on:    %s (last upgrade)\n", stamp)
			} else if info.BuiltVersion != "" {
				fmt.Printf("built on:    %s\n", info.BuiltVersion)
			}
			fmt.Printf("state:       %s (desired)\n", info.State)
			fmt.Printf("container:   %s\n", info.Container)
			// Surface version-skew / dead-heartbeat right where the operator inspects
			// an island, with the exact remedy inline.
			if note := islandSkewNote(*info, daemonVersion(cmd.Context(), c)); note != "" {
				fmt.Printf("skew:        %s\n", note)
			}
			if info.Owner != "" {
				fmt.Printf("owner:       %s\n", info.Owner)
			}
			if len(info.Tags) > 0 {
				fmt.Printf("tags:        %s\n", formatTags(info.Tags))
			}
			if !info.CreatedAt.IsZero() {
				fmt.Printf("created:     %s\n", info.CreatedAt.Local().Format(time.RFC3339))
			}
			if info.Stats != nil {
				fmt.Printf("memory:      %s / %s\n", humanBytes(info.Stats.MemoryUsageBytes), humanBytes(info.Stats.MemoryLimitBytes))
				fmt.Printf("cpu:         %.1f%%\n", info.Stats.CPUPercent)
			}
			if info.Disk != nil && info.Disk.TotalBytes > 0 {
				fmt.Printf("disk:        %s (workspace %s · home %s)\n",
					humanBytes(uint64(info.Disk.TotalBytes)), humanBytes(uint64(info.Disk.WorkspaceBytes)),
					humanBytes(uint64(info.Disk.HomeBytes)))
			}
			if info.AgentState != nil {
				fmt.Printf("agent:       %s (%s ago)\n", info.AgentState.Latest, time.Since(info.AgentState.UpdatedAt).Round(time.Second))
			}
			if info.Git != nil {
				clean := "dirty"
				if info.Git.Clean {
					clean = "clean"
				}
				fmt.Printf("git:         %s · %s", info.Git.Branch, clean)
				if !info.Git.Clean {
					fmt.Printf(" (%d files)", info.Git.DirtyFiles)
				}
				if info.Git.Ahead > 0 {
					fmt.Printf(" · %d ahead", info.Git.Ahead)
				}
				if info.Git.Behind > 0 {
					fmt.Printf(" · %d behind", info.Git.Behind)
				}
				fmt.Println()
			}
			if len(info.Attached) > 0 {
				labels := make([]string, 0, len(info.Attached))
				for _, a := range info.Attached {
					labels = append(labels, a.Label)
				}
				fmt.Printf("attached:    %s\n", strings.Join(labels, ", "))
			}
			return nil
		},
	}
}

// --- overview ------------------------------------------------------------

func newOverviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "overview",
		Short: "Server-wide totals: islands, memory, cpu, daemon uptime.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			o, err := c.Overview(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("islands:     %d (%d running · %d hibernated · %d errored)\n",
				o.TotalIslands, o.Running, o.Hibernated, o.Errored)
			fmt.Printf("attached:    %d client(s) across all islands\n", o.AttachedClients)
			if o.MemoryUsageBytes > 0 {
				fmt.Printf("memory:      %s used\n", humanBytes(o.MemoryUsageBytes))
			}
			if o.CPUPercent > 0 {
				fmt.Printf("cpu:         %.1f%% (total across running islands)\n", o.CPUPercent)
			}
			fmt.Printf("webhooks:    %d subscription(s)\n", o.WebhookCount)
			fmt.Printf("daemon up:   %s (since %s)\n",
				time.Since(o.DaemonStartedAt).Round(time.Second),
				o.DaemonStartedAt.Local().Format(time.RFC3339))
			return nil
		},
	}
}

// humanBytes formats a byte count as e.g. "1.2 GiB".
func humanBytes(b uint64) string {
	if b == 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// --- purge ----------------------------------------------------------------

func newPurgeCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "purge <name>",
		Short: "Destroy an island and its data.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !force {
				fmt.Printf("This will destroy island %q AND its workspace + agent state volumes.\n", name)
				fmt.Print("Type the island name to confirm: ")
				var confirm string
				_, _ = fmt.Scanln(&confirm)
				if strings.TrimSpace(confirm) != name {
					return fmt.Errorf("aborted")
				}
			}
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.DeleteIsland(cmd.Context(), name, force); err != nil {
				return err
			}
			fmt.Printf("purged %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation and the unpushed-work guard")
	return cmd
}

// parseTags turns repeated --tag key=value flags into a map, rejecting entries
// without a "=" or with an empty key.
func parseTags(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	tags := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --tag %q (want key=value)", p)
		}
		tags[k] = strings.TrimSpace(v)
	}
	return tags, nil
}

// formatTags renders a tag map as "k=v k=v" in sorted key order (stable output).
func formatTags(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+tags[k])
	}
	return strings.Join(parts, " ")
}

// defaultOwner derives a creator label as "<user>@<host>" for attribution,
// falling back gracefully when either lookup fails.
func defaultOwner() string {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return name + "@" + host
	}
	return name
}

// countNoun renders a count with a singular/plural noun: countNoun(1, "island")
// → "1 island", countNoun(0, "island") → "0 islands".
func countNoun(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// --- uninstall ------------------------------------------------------------
// newUninstallCmd and islandAtRisk live in uninstall.go.

// --- panic ----------------------------------------------------------------

func newPanicCmd() *cobra.Command {
	var clear, status bool
	var reason string
	cmd := &cobra.Command{
		Use:   "panic",
		Short: "Stop every island now and block auto-restart (emergency brake).",
		Long: "Immediately stops every running island and writes a ~/.dejima/PANIC flag so the\n" +
			"daemon will not auto-start any island — even across a daemon restart — until the\n" +
			"flag is cleared. Volumes and desired state are preserved.\n\n" +
			"  dejima panic              engage: stop everything, set the flag\n" +
			"  dejima panic --clear      clear the flag and restart islands meant to be running\n" +
			"  dejima panic --status     report whether panic mode is engaged",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			switch {
			case status:
				st, err := c.PanicStatus(ctx)
				if err != nil {
					return err
				}
				if st.Panicked {
					msg := "panic: ENGAGED — islands are stopped; `dejima panic --clear` to resume"
					if st.Reason != "" {
						msg += "\nreason: " + st.Reason
					}
					fmt.Println(msg)
				} else {
					fmt.Println("panic: not engaged")
				}
				return nil
			case clear:
				// Restart-all can take a while; bound it generously — the client now
				// honors the context deadline rather than the 30s default.
				lctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
				st, err := c.ClearPanic(lctx)
				if err != nil {
					return err
				}
				fmt.Printf("panic cleared — restarted %s\n", countNoun(st.Affected, "island"))
				return nil
			default:
				// Stop-all sweeps every island; allow more than the 30s default.
				lctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
				st, err := c.Panic(lctx, reason)
				if err != nil {
					return err
				}
				fmt.Printf("PANIC engaged — stopped %s; auto-restart blocked until `dejima panic --clear`\n",
					countNoun(st.Affected, "island"))
				return nil
			}
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the PANIC flag and restart islands meant to be running")
	cmd.Flags().BoolVar(&status, "status", false, "report whether panic mode is engaged")
	cmd.Flags().StringVar(&reason, "reason", "", "note recorded with the panic flag")
	return cmd
}

func shortenRepo(repo string) string {
	if len(repo) <= 50 {
		return repo
	}
	return "..." + repo[len(repo)-47:]
}
