// Command dejima is the Dejima CLI — a thin client of the Dejima API.
package main

import (
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
		os.Exit(1)
	}
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
	cmd.AddCommand(
		newInitCmd(),
		newHomeCmd(),
		newConnectCmd(),
		newLsCmd(),
		newAgentCmd(),
		newTermCmd(),
		newStatusCmd(),
		newHibernateCmd(),
		newWakeCmd(),
		newResetCmd(),
		newPurgeCmd(),
		newPanicCmd(),
		newUninstallCmd(),
		newCloneCmd(),
		newUpgradeCmd(),
		newExecCmd(),
		newCpCmd(),
		newPortCmd(),
		newCapCmd(),
		newAuditCmd(),
		newLogsCmd(),
		newImageCmd(),
		newServiceCmd(),
		newSSHCmd(),
		newWebhookCmd(),
		newAuthCmd(),
		newLogoutAllCmd(),
		newClientsCmd(),
		newOverviewCmd(),
		newDoctorCmd(),
		newOnboardCmd(),
		newUpdateCmd(),
		newTUICmd(),
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
		Long: "Open an interactive PTY into the island's tmux session via the Dejima API. " +
			"Multiple clients can attach simultaneously (shared tmux). Disconnect with the " +
			"normal tmux detach key (Ctrl-b then d).\n\n" +
			"For islands with multiple agents, target one with --agent <id> or the " +
			"`<name>/<agent>` shorthand; the bare name attaches to the primary agent.",
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
			return runSession(cmd.Context(), c, name, agent, label)
		},
	}
	cmd.Flags().StringVar(&label, "as", "", "client label shown in presence (default: $HOSTNAME or 'cli')")
	cmd.Flags().StringVar(&agentID, "agent", "", "agent id to attach to (default: the island's primary agent)")
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
func runSession(ctx context.Context, c *api.Client, name, agentID, label string) error {
	conn, err := c.DialAgentSession(ctx, name, agentID, label)
	if err != nil {
		return err
	}
	return runSessionConn(ctx, conn)
}

// runTerminalSession attaches the local terminal to a host terminal's session.
func runTerminalSession(ctx context.Context, c *api.Client, id, label string) error {
	conn, err := c.DialTerminalSession(ctx, id, label)
	if err != nil {
		return err
	}
	return runSessionConn(ctx, conn)
}

// runSessionConn bridges the local terminal to an already-dialed session
// websocket — an island agent or a host terminal — until detach or EOF.
func runSessionConn(ctx context.Context, conn *websocket.Conn) error {
	defer conn.Close(websocket.StatusNormalClosure, "")
	var err error

	// Surface the detach hint before raw mode swallows newlines.
	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		fmt.Fprintln(os.Stderr, "[dejima] attached. Detach: Ctrl-b d (tmux), or just close the terminal. Session keeps running either way.")
	}

	// Enter raw mode on stdin so keystrokes pass through to the agent unfiltered.
	var oldState *term.State
	if term.IsTerminal(stdinFd) {
		oldState, err = term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("raw mode: %w", err)
		}
		defer func() { _ = term.Restore(stdinFd, oldState) }()
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Send an initial resize so tmux opens at the right dimensions.
	if rows, cols, err := terminalSize(stdinFd); err == nil {
		_ = writeEnvelope(sessionCtx, conn, api.SessionEnvelope{Type: "resize", Rows: rows, Cols: cols})
	}

	// Forward terminal resizes for the life of the session. The mechanism is
	// OS-specific (SIGWINCH on Unix, polling on Windows) — see resize_*.go.
	watchTerminalResize(sessionCtx, stdinFd, func(rows, cols uint16) {
		_ = writeEnvelope(sessionCtx, conn, api.SessionEnvelope{Type: "resize", Rows: rows, Cols: cols})
	})

	// Server → stdout pump.
	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(sessionCtx)
			if err != nil {
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
				raw, derr := base64StdDecode(env.B64)
				if derr != nil {
					continue
				}
				_, _ = os.Stdout.Write(raw)
			case "error":
				fmt.Fprintln(os.Stderr, "server error:", env.B64)
				return
			}
		}
	}()

	// Stdin → server pump.
	buf := make([]byte, 4096)
	for {
		select {
		case <-sessionCtx.Done():
			return nil
		default:
		}
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			env := api.SessionEnvelope{Type: "data", B64: base64StdEncode(buf[:n])}
			if werr := writeEnvelope(sessionCtx, conn, env); werr != nil {
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
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
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			writeRow := func(i api.IslandInfo) {
				agentCol := i.Agent
				if len(i.Agents) > 1 {
					agentCol = fmt.Sprintf("%d agents", len(i.Agents))
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					i.Name, agentCol, shortenRepo(i.Repo), i.State, i.Container)
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
					fmt.Fprintf(tw, "%s\t\t\t\t(%s)\n", shortenRepo(g.repo), countNoun(len(g.islands), "island"))
					for _, i := range g.islands {
						writeRow(i)
					}
				}
				return tw.Flush()
			}

			fmt.Fprintln(tw, "NAME\tAGENT\tREPO\tSTATE\tCONTAINER")
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
	cmd.AddCommand(newAgentLsCmd(), newAgentAddCmd(), newAgentRmCmd())
	return cmd
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
	var typ, label string
	cmd := &cobra.Command{
		Use:   "add <island>",
		Short: "Add an agent to an island.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			a, err := c.AddAgent(cmd.Context(), args[0], api.AgentSpecRequest{Type: typ, Label: label})
			if err != nil {
				return err
			}
			fmt.Printf("added agent %s (%s) to %s — attach with `dejima connect %s/%s`\n",
				a.ID, a.Type, args[0], args[0], a.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "agent type (default: same as the island's primary agent)")
	cmd.Flags().StringVar(&label, "label", "", "optional label for the agent")
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
			fmt.Printf("state:       %s (desired)\n", info.State)
			fmt.Printf("container:   %s\n", info.Container)
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

// islandAtRisk mirrors the daemon's purge guard so `dejima uninstall` can
// pre-flight the whole batch and refuse before purging anything. Returns a
// human reason when the island has at-risk work (or can't be verified), or ""
// when it's safe to purge.
func islandAtRisk(ctx context.Context, c *api.Client, isl api.IslandInfo) string {
	if isl.Container != "running" {
		return "not running — unpushed work can't be verified"
	}
	d, err := c.GetIsland(ctx, isl.Name)
	if err != nil || d.Git == nil {
		return "" // no git info → nothing git-tracked to lose
	}
	var risks []string
	if !d.Git.Clean && d.Git.DirtyFiles > 0 {
		risks = append(risks, countNoun(d.Git.DirtyFiles, "uncommitted change"))
	}
	if d.Git.Ahead > 0 {
		risks = append(risks, countNoun(d.Git.Ahead, "unpushed commit"))
	}
	return strings.Join(risks, " and ")
}

func newUninstallCmd() *cobra.Command {
	var yes, force, systemSvc, keepData bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Dejima entirely: purge all islands, uninstall the service, delete binaries + ~/.dejima.",
		Long: "One-shot clean removal: purges every island (honoring the unpushed-work guard),\n" +
			"uninstalls the dejimad service, removes the dejima/dejimad binaries, and deletes\n" +
			"~/.dejima. Destructive and irreversible — confirms first unless --yes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			islands, err := c.ListIslands(ctx)
			if err != nil {
				return fmt.Errorf("list islands: %w (is the daemon running?)", err)
			}

			// Pre-flight the unpushed-work guard across ALL islands first, so we
			// never purge some and then abort on a guarded one (half-uninstall).
			if !force {
				var atRisk []string
				for _, isl := range islands {
					if reason := islandAtRisk(ctx, c, isl); reason != "" {
						atRisk = append(atRisk, fmt.Sprintf("  %s — %s", isl.Name, reason))
					}
				}
				if len(atRisk) > 0 {
					return fmt.Errorf("refusing to uninstall — these islands have at-risk or unverifiable work:\n%s\n\ncommit/push (or `dejima wake` to verify), or re-run with --force",
						strings.Join(atRisk, "\n"))
				}
			}

			selfBin, _ := os.Executable()
			daemonBin, _ := resolveDaemonBinary()
			root, _ := paths.Root()

			fmt.Println("This will permanently:")
			fmt.Printf("  • purge %s (and all their volumes)\n", countNoun(len(islands), "island"))
			fmt.Println("  • uninstall the dejimad service")
			if daemonBin != "" {
				fmt.Printf("  • remove %s\n", daemonBin)
			}
			if selfBin != "" {
				fmt.Printf("  • remove %s\n", selfBin)
			}
			if !keepData && root != "" {
				fmt.Printf("  • delete %s\n", root)
			}
			fmt.Println()

			if !yes {
				fmt.Print("Type 'uninstall' to confirm: ")
				var in string
				_, _ = fmt.Scanln(&in)
				if strings.TrimSpace(in) != "uninstall" {
					return fmt.Errorf("aborted")
				}
			}

			// 1. Purge every island. The guard pre-flight already ran, so force.
			for _, isl := range islands {
				if err := c.DeleteIsland(ctx, isl.Name, true); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: purge %s: %v\n", isl.Name, err)
				} else {
					fmt.Printf("  purged %s\n", isl.Name)
				}
			}

			// 2. Uninstall the service (stops the daemon).
			if mgr, mErr := serviceMgr(systemSvc); mErr == nil {
				if err := mgr.Uninstall(); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: service uninstall: %v\n", err)
				} else {
					fmt.Println("  service uninstalled")
				}
			}

			// 3. Remove the binaries. A running binary can be unlinked on unix
			// (the inode lives until the process exits).
			for _, bin := range []string{daemonBin, selfBin} {
				if bin == "" {
					continue
				}
				switch err := os.Remove(bin); {
				case err == nil:
					fmt.Printf("  removed %s\n", bin)
				case errors.Is(err, os.ErrNotExist):
					// already gone
				case errors.Is(err, os.ErrPermission):
					fmt.Fprintf(os.Stderr, "  note: couldn't remove %s (permission) — `sudo rm %s`\n", bin, bin)
				default:
					fmt.Fprintf(os.Stderr, "  warning: remove %s: %v\n", bin, err)
				}
			}

			// 4. Delete ~/.dejima.
			if !keepData && root != "" {
				if err := os.RemoveAll(root); err != nil {
					fmt.Fprintf(os.Stderr, "  note: couldn't fully delete %s: %v (root-owned files? `sudo rm -rf %s`)\n", root, err, root)
				} else {
					fmt.Printf("  deleted %s\n", root)
				}
			}

			fmt.Println("\nDejima uninstalled.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "bypass the unpushed-work guard (purge islands even with unpushed/uncommitted work)")
	cmd.Flags().BoolVar(&systemSvc, "system", false, "uninstall the system-wide LaunchDaemon (macOS)")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "keep ~/.dejima (config, GitHub identities, ledger)")
	return cmd
}

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
				st, err := c.ClearPanic(ctx)
				if err != nil {
					return err
				}
				fmt.Printf("panic cleared — restarted %s\n", countNoun(st.Affected, "island"))
				return nil
			default:
				st, err := c.Panic(ctx, reason)
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
