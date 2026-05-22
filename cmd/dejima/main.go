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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/service"
	"github.com/aoos/dejima/internal/version"
)

// execLookPath is a small indirection so resolveDaemonBinary stays testable.
var execLookPath = exec.LookPath

func base64StdEncode(b []byte) string         { return base64.StdEncoding.EncodeToString(b) }
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
	}
	cmd.AddCommand(
		newInitCmd(),
		newConnectCmd(),
		newLsCmd(),
		newStatusCmd(),
		newHibernateCmd(),
		newWakeCmd(),
		newResetCmd(),
		newPurgeCmd(),
		newExecCmd(),
		newCpCmd(),
		newLogsCmd(),
		newServiceCmd(),
		newWebhookCmd(),
		newDoctorCmd(),
	)
	return cmd
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
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Tail an island's container logs.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			rc, err := c.StreamLogs(cmd.Context(), args[0], follow)
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(os.Stdout, rc)
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new output until interrupted")
	return cmd
}

// --- service --------------------------------------------------------------

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install or uninstall dejimad as a host service.",
	}
	var notifyURL, notifySecret string
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install dejimad as a launchd (macOS) or systemd-user (Linux) service.",
		Long: "Registers dejimad with the host service manager so it survives reboots. " +
			"Optionally subscribes a webhook for state-change notifications in the same step.",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := serviceMgr()
			if err != nil {
				return err
			}
			bin, err := resolveDaemonBinary()
			if err != nil {
				return err
			}
			if err := mgr.Install(bin); err != nil {
				return err
			}
			fmt.Printf("installed dejimad service (binary: %s)\n", bin)
			if notifyURL != "" {
				if err := waitForDaemonAndSubscribe(cmd.Context(), notifyURL, notifySecret); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not auto-subscribe webhook (%v)\n", err)
					fmt.Fprintf(os.Stderr, "  run later: dejima webhook subscribe --url %s\n", notifyURL)
				} else {
					fmt.Printf("subscribed webhook: %s\n", notifyURL)
				}
			}
			return nil
		},
	}
	installCmd.Flags().StringVar(&notifyURL, "notify", "", "auto-subscribe this webhook URL after install (e.g. https://ntfy.sh/your-topic)")
	installCmd.Flags().StringVar(&notifySecret, "notify-secret", "", "HMAC secret for the auto-subscribed webhook")
	cmd.AddCommand(
		installCmd,
		&cobra.Command{
			Use:   "uninstall",
			Short: "Remove the dejimad service.",
			RunE: func(cmd *cobra.Command, args []string) error {
				mgr, err := serviceMgr()
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
			Use:   "status",
			Short: "Report whether the dejimad service is loaded.",
			RunE: func(cmd *cobra.Command, args []string) error {
				mgr, err := serviceMgr()
				if err != nil {
					return err
				}
				s, err := mgr.Status()
				if err != nil {
					return err
				}
				fmt.Println(s)
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
	subscribe := &cobra.Command{
		Use:   "subscribe",
		Short: "Subscribe a URL to receive event POSTs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				return fmt.Errorf("--url is required")
			}
			c, err := client()
			if err != nil {
				return err
			}
			sub, err := c.SubscribeWebhook(cmd.Context(), url, secret)
			if err != nil {
				return err
			}
			fmt.Printf("subscribed: %s -> %s\n", sub.ID, sub.URL)
			return nil
		},
	}
	subscribe.Flags().StringVar(&url, "url", "", "webhook URL (required)")
	subscribe.Flags().StringVar(&secret, "secret", "", "HMAC secret signed into the X-Dejima-Signature header")

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
				fmt.Printf("%s\t%s\n", s.ID, s.URL)
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

	cmd.AddCommand(subscribe, list, unsubscribe)
	return cmd
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

func client() (*api.Client, error) {
	if h := os.Getenv("DEJIMA_HOST"); h != "" {
		return api.NewTCPClient(h)
	}
	return api.NewUnixClient()
}

func serviceMgr() (service.Manager, error) {
	return service.New()
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
				_, subErr := c.SubscribeWebhook(ctx, url, secret)
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
		repo   string
		name   string
		agent  string
		image  string
		memory string
		cpus   string
		disk   string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Provision a new island.",
		Long: "Create a contained workspace for an agent. The repo is cloned into a " +
			"persistent volume inside the island; the agent is started in a tmux session.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			if name == "" {
				name = project.DeriveNameFromRepo(repo)
			}
			c, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			info, err := c.CreateIsland(ctx, api.CreateIslandRequest{
				Name:  name,
				Repo:  repo,
				Agent: agent,
				Image: image,
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
			fmt.Printf("connect: dejima connect %s\n", info.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "git repo URL or local path (required)")
	cmd.Flags().StringVar(&name, "name", "", "island name (default: derived from repo)")
	cmd.Flags().StringVar(&agent, "agent", "", "agent to run (default: claude-code)")
	cmd.Flags().StringVar(&image, "image", "", "island image (default: dejima/island:latest)")
	cmd.Flags().StringVar(&memory, "memory", "", "memory limit (e.g. 4G); default: unlimited")
	cmd.Flags().StringVar(&cpus, "cpus", "", "CPU limit (e.g. 2.0); default: unlimited")
	cmd.Flags().StringVar(&disk, "disk", "", "disk size (e.g. 20G); default: unlimited")
	return cmd
}

// --- connect --------------------------------------------------------------

func newConnectCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "connect <name>",
		Short: "Attach to an island's session.",
		Long: "Open an interactive PTY into the island's tmux session via the Dejima API. " +
			"Multiple clients can attach simultaneously (shared tmux). Disconnect with the " +
			"normal tmux detach key (Ctrl-b then d).",
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
			return runSession(cmd.Context(), c, name, label)
		},
	}
	cmd.Flags().StringVar(&label, "as", "", "client label shown in presence (default: $HOSTNAME or 'cli')")
	return cmd
}

// defaultLabel produces a client label for presence: $HOSTNAME or "cli".
func defaultLabel() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "cli"
}

// runSession is the websocket ↔ local-stdio bridge driving `dejima connect`.
func runSession(ctx context.Context, c *api.Client, name, label string) error {
	conn, err := c.DialSession(ctx, name, label)
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

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

	// SIGWINCH handler: forward terminal resizes.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-winch:
				if rows, cols, err := terminalSize(stdinFd); err == nil {
					_ = writeEnvelope(sessionCtx, conn, api.SessionEnvelope{Type: "resize", Rows: rows, Cols: cols})
				}
			}
		}
	}()

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
	w, h, err := term.GetSize(fd)
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
	return &cobra.Command{
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
			fmt.Fprintln(tw, "NAME\tAGENT\tREPO\tSTATE\tCONTAINER")
			for _, i := range items {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					i.Name, i.Agent, shortenRepo(i.Repo), i.State, i.Container)
			}
			return tw.Flush()
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
			if !info.CreatedAt.IsZero() {
				fmt.Printf("created:     %s\n", info.CreatedAt.Local().Format(time.RFC3339))
			}
			if info.Stats != nil {
				fmt.Printf("memory:      %s / %s\n", humanBytes(info.Stats.MemoryUsageBytes), humanBytes(info.Stats.MemoryLimitBytes))
				fmt.Printf("cpu:         %.1f%%\n", info.Stats.CPUPercent)
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
			if err := c.DeleteIsland(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Printf("purged %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	return cmd
}

func shortenRepo(repo string) string {
	if len(repo) <= 50 {
		return repo
	}
	return "..." + repo[len(repo)-47:]
}
