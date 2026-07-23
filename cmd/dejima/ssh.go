package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/sshfacade"
)

// newSSHCmd manages the SSH-façade: which public keys may ssh into an island,
// and how to connect. These are host-local operations on ~/.dejima (the daemon
// host owns the authorized_keys and host key), so run them where dejimad runs.
func newSSHCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh",
		Short: "Manage SSH access into islands (the daemon SSH-façade)",
		Long: "The daemon is the single SSH endpoint for every island: `ssh <island>@<daemon-host> -p <port>`\n" +
			"authenticates with a per-island public key and lands you in the container (VS Code / Cursor\n" +
			"Remote-SSH and framework SSH backends work the same way). Start the listener with\n" +
			"`dejimad --ssh <addr>`. These subcommands manage the authorized keys host-side.\n\n" +
			"Dead-simple editor setup:  dejima ssh authorize <island> --key ~/.ssh/id_ed25519.pub\n" +
			"                           dejima ssh config <island> --install\n" +
			"then in VS Code / Cursor:  Remote-SSH: Connect to Host… → dejima-<island>",
	}
	cmd.AddCommand(newSSHEnrollCmd(), newSSHAuthorizeCmd(), newSSHListCmd(), newSSHRevokeCmd(), newSSHInfoCmd(), newSSHConfigCmd())
	return cmd
}

// newSSHEnrollCmd is the dead-simple, no-copy-paste path: register THIS device's
// key with the daemon over the API (works local or remote — the daemon performs
// the write, so there's no copying keys to the daemon host and no user-vs-root
// file-ownership snag), then write the local ~/.ssh/config for every island.
func newSSHEnrollCmd() *cobra.Command {
	var keyFile string
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Authorize THIS device with the daemon and set up your editor (no copy-paste)",
		Long: "Registers this machine's SSH public key with the daemon via the API — from any\n" +
			"operator device, local or remote, without copying keys to the daemon host — then writes\n" +
			"~/.ssh/config entries for every island. After this: VS Code/Cursor Remote-SSH → dejima-<island>.\n" +
			"Generates an ed25519 key if this machine has none.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pub := keyFile
			if pub == "" {
				p, err := ensureLocalKey()
				if err != nil {
					return err
				}
				pub = p
			}
			line, err := os.ReadFile(pub)
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			fp, err := c.AuthorizeAccountKey(cmd.Context(), strings.TrimSpace(string(line)))
			if err != nil {
				return fmt.Errorf("enroll key with daemon: %w", err)
			}
			fmt.Printf("enrolled this device (%s) — authorized for every island\n", fp)

			// Write local ssh config for all islands so the editor list is ready.
			host, port, enabled, rerr := resolveSSHEndpoint(cmd.Context())
			if rerr != nil || !enabled {
				fmt.Println("key authorized — but the SSH façade isn't enabled on the daemon, so editor")
				fmt.Println("config was skipped. Enable it on the daemon HOST (needs sudo):")
				fmt.Println()
				enable, _ := enableFacadeCommand()
				fmt.Println("  " + enable)
				fmt.Println()
				fmt.Println("then re-run `dejima ssh enroll` here.")
				return nil
			}
			islands, lerr := c.ListIslands(cmd.Context())
			if lerr != nil {
				return lerr
			}
			for _, isl := range islands {
				if err := installSSHConfig(isl.Name, sshConfigBlock(isl.Name, host, port)); err != nil {
					return err
				}
			}
			if len(islands) == 0 {
				fmt.Println("(no islands yet — create one, then `dejima ssh config <name> --install`)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&keyFile, "key", "", "enroll this public-key file instead of the default ~/.ssh/id_ed25519.pub")
	return cmd
}

// ensureLocalKey returns this machine's default SSH public key path, generating
// an ed25519 keypair if none exists.
func ensureLocalKey() (string, error) {
	if p, err := defaultPublicKey(); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	priv := filepath.Join(dir, "id_ed25519")
	fmt.Println("no SSH key on this machine — generating an ed25519 keypair…")
	gen := exec.Command("ssh-keygen", "-t", "ed25519", "-f", priv, "-N", "", "-q")
	gen.Stdout, gen.Stderr = os.Stdout, os.Stderr
	if err := gen.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %w", err)
	}
	return priv + ".pub", nil
}

func newSSHAuthorizeCmd() *cobra.Command {
	var keyFile string
	var account bool
	cmd := &cobra.Command{
		Use:   "authorize [island] [public-key-line]",
		Short: "Authorize a public key to ssh into an island (or --account, every island)",
		Long: "Adds an OpenSSH public key to an island's authorized_keys. Provide the key as an\n" +
			"argument, via --key <file>, or on stdin:\n\n" +
			"  dejima ssh authorize myisland --key ~/.ssh/id_ed25519.pub\n" +
			"  cat ~/.ssh/id_ed25519.pub | dejima ssh authorize myisland\n\n" +
			"With --account the key is registered fleet-wide: it authorizes EVERY island,\n" +
			"current and future, with no per-island step (use this for your own devices):\n\n" +
			"  dejima ssh authorize --account --key ~/.ssh/id_ed25519.pub",
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if account {
				// account-wide: args are key-only (0 or 1 positional).
				if len(args) > 1 {
					return fmt.Errorf("--account takes no island; pass only the key (or --key/stdin)")
				}
				positional := ""
				if len(args) == 1 {
					positional = args[0]
				}
				line, err := resolveKeyLine(cmd, positional, keyFile)
				if err != nil {
					return err
				}
				fp, err := sshfacade.AddAccountKey(line)
				if err != nil {
					return err
				}
				fmt.Printf("authorized %s for ALL islands (account-wide)\n", fp)
				fmt.Println("set up your editor for every island:  dejima ssh config --all")
				return nil
			}
			if len(args) < 1 {
				return fmt.Errorf("give an island name, or --account to authorize every island")
			}
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			positional := ""
			if len(args) == 2 {
				positional = args[1]
			}
			line, err := resolveKeyLine(cmd, positional, keyFile)
			if err != nil {
				return err
			}
			fp, err := sshfacade.AddAuthorizedKey(island, line)
			if err != nil {
				return err
			}
			fmt.Printf("authorized %s for island %q\n", fp, island)
			printConnectHint(cmd.Context(), island)
			return nil
		},
	}
	cmd.Flags().StringVar(&keyFile, "key", "", "read the public key from this file")
	cmd.Flags().BoolVar(&account, "account", false, "register the key fleet-wide (authorizes every island, current and future)")
	return cmd
}

func newSSHListCmd() *cobra.Command {
	var account bool
	cmd := &cobra.Command{
		Use:   "list [island]",
		Short: "List authorized keys for an island (or --account, the fleet-wide keys)",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if account {
				keys, err := sshfacade.ListAccountKeys()
				if err != nil {
					return err
				}
				if len(keys) == 0 {
					fmt.Println("no account-wide keys (authorize one: dejima ssh authorize --account --key ~/.ssh/id_ed25519.pub)")
					return nil
				}
				for _, k := range keys {
					fmt.Printf("%s  %-12s %s\n", k.Fingerprint, k.Type, k.Comment)
				}
				return nil
			}
			if len(args) < 1 {
				return fmt.Errorf("give an island name, or --account to list fleet-wide keys")
			}
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			keys, err := sshfacade.ListAuthorizedKeys(island)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Printf("no per-island keys for %q (account-wide keys, if any, still apply — see `dejima ssh list --account`)\n", island)
				return nil
			}
			for _, k := range keys {
				fmt.Printf("%s  %-12s %s\n", k.Fingerprint, k.Type, k.Comment)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&account, "account", false, "list the fleet-wide (account) keys instead of an island's")
	return cmd
}

func newSSHRevokeCmd() *cobra.Command {
	var all, account bool
	cmd := &cobra.Command{
		Use:   "revoke [island] [fingerprint]",
		Short: "Revoke an SSH key from an island (or --account, fleet-wide)",
		Long: "Removes an authorized key by its SHA256 fingerprint (see `dejima ssh list`), or\n" +
			"--all to revoke every key. With --account, operates on the fleet-wide allow-set —\n" +
			"revoking there removes access to every island at once.",
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				n   int
				err error
			)
			if account {
				// account-wide: optional fingerprint is the sole positional.
				switch {
				case all:
					n, err = sshfacade.RemoveAllAccountKeys()
				case len(args) >= 1:
					n, err = sshfacade.RemoveAccountKey(args[0])
				default:
					return fmt.Errorf("give a fingerprint to revoke (from `dejima ssh list --account`), or --all")
				}
				if err != nil {
					return err
				}
				if n == 0 {
					return fmt.Errorf("no matching account-wide key (nothing revoked)")
				}
				fmt.Printf("revoked %d account-wide key(s) — fleet-wide access removed\n", n)
				return nil
			}
			if len(args) < 1 {
				return fmt.Errorf("give an island name, or --account to revoke fleet-wide keys")
			}
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			switch {
			case all:
				n, err = sshfacade.RemoveAllAuthorizedKeys(island)
			case len(args) == 2:
				n, err = sshfacade.RemoveAuthorizedKey(island, args[1])
			default:
				return fmt.Errorf("give a fingerprint to revoke (from `dejima ssh list`), or --all")
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("no matching key found for %q (nothing revoked)", island)
			}
			fmt.Printf("revoked %d key(s) from %q\n", n, island)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "revoke every key (for the island, or fleet-wide with --account)")
	cmd.Flags().BoolVar(&account, "account", false, "operate on the fleet-wide (account) allow-set")
	return cmd
}

func newSSHInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <island>",
		Short: "Print how to ssh into an island",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			signer, err := sshfacade.HostSigner()
			if err != nil {
				return err
			}
			host, port, enabled, _ := resolveSSHEndpoint(cmd.Context())
			fmt.Printf("island:        %s\n", island)
			if enabled {
				fmt.Printf("connect:       ssh %s@%s -p %s\n", island, host, port)
				fmt.Printf("VS Code/Cursor: dejima ssh config %s --install\n", island)
			} else {
				fmt.Printf("connect:       ssh %s@<daemon-host> -p <ssh-port>\n", island)
				fmt.Println("listener:      OFF — start it with `dejimad --ssh <addr>` or `dejima service install --ssh :2222`")
			}
			fmt.Printf("host key:      %s\n", sshfacade.Fingerprint(signer))
			fmt.Printf("authorize a key:  dejima ssh authorize %s --key ~/.ssh/id_ed25519.pub\n", island)
			return nil
		},
	}
}

// newSSHConfigCmd emits (or installs) a ready ~/.ssh/config entry. VS Code and
// Cursor both read ~/.ssh/config and list every Host in their Remote-SSH picker,
// so this is the dead-simple path: authorize a key, run `config --install`, then
// pick `dejima-<island>` in the editor.
func newSSHConfigCmd() *cobra.Command {
	var install, all bool
	cmd := &cobra.Command{
		Use:   "config [island]",
		Short: "Print or install ~/.ssh/config entries for VS Code / Cursor Remote-SSH",
		Long: "Generates ssh config Host blocks aliased `dejima-<island>`, resolving the real\n" +
			"connection address from the daemon (tailnet host when the listener binds a wildcard).\n" +
			"With --install it appends to ~/.ssh/config (idempotent); without it, prints the blocks.\n" +
			"With --all it does this for every island at once, so your whole fleet shows up in\n" +
			"Remote-SSH: Connect to Host…\n\n" +
			"  dejima ssh config myisland --install\n" +
			"  dejima ssh config --all --install",
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, port, enabled, err := resolveSSHEndpoint(cmd.Context())
			if err != nil {
				return err
			}
			if !enabled {
				return fmt.Errorf("%s", sshFacadeSetupSteps())
			}

			// Resolve the target island list: --all = every island; else the one arg.
			var names []string
			if all {
				c, cerr := client()
				if cerr != nil {
					return cerr
				}
				islands, lerr := c.ListIslands(cmd.Context())
				if lerr != nil {
					return lerr
				}
				for _, isl := range islands {
					names = append(names, isl.Name)
				}
				if len(names) == 0 {
					fmt.Println("no islands yet — create one with `dejima init`")
					return nil
				}
			} else {
				if len(args) < 1 {
					return fmt.Errorf("give an island name, or --all for every island")
				}
				if _, err := project.Load(args[0]); err != nil {
					return err
				}
				names = []string{args[0]}
			}

			if !install {
				for _, name := range names {
					fmt.Print(sshConfigBlock(name, host, port))
				}
				fmt.Fprintln(os.Stderr,
					"\n# add the above to ~/.ssh/config (or re-run with --install), then in VS Code/Cursor:\n"+
						"#   Remote-SSH: Connect to Host… → dejima-<island>")
				return nil
			}
			for _, name := range names {
				if err := installSSHConfig(name, sshConfigBlock(name, host, port)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "append the entry(ies) to ~/.ssh/config (idempotent)")
	cmd.Flags().BoolVar(&all, "all", false, "generate entries for every island")
	return cmd
}

// sshConfigBlock renders an ~/.ssh/config Host stanza. The alias is namespaced
// `dejima-<island>` so it sorts together in the editor picker and won't collide
// with the user's own hosts.
func sshConfigBlock(island, host, port string) string {
	return fmt.Sprintf("Host dejima-%s\n"+
		"    HostName %s\n"+
		"    Port %s\n"+
		"    User %s\n",
		island, host, port, island)
}

// installSSHConfig is the CLI wrapper: it mutates ~/.ssh/config via
// writeSSHConfigEntry and prints what happened.
func installSSHConfig(island, block string) error {
	status, path, err := writeSSHConfigEntry(island, block)
	if err != nil {
		return err
	}
	if status == "exists" {
		fmt.Printf("~/.ssh/config already has Host dejima-%s (left unchanged)\n", island)
	} else {
		fmt.Printf("added Host dejima-%s to %s\n", island, path)
	}
	fmt.Printf("VS Code / Cursor → Remote-SSH: Connect to Host… → dejima-%s\n", island)
	// The dead-simple path: open straight into the repo, no folder-browsing (the
	// SSH home is /home/<island>, which only holds dotfiles — the working tree is
	// /workspace). VS Code remembers the folder per host after the first open.
	fmt.Printf("   or open the repo directly:  code --remote ssh-remote+dejima-%s /workspace\n", island)
	return nil
}

// writeSSHConfigEntry appends the block to ~/.ssh/config unless an entry for the
// island is already present (so re-running is safe and won't duplicate). Returns
// "added" or "exists" and the config path. No output — callers report (the CLI
// prints; the TUI sets a status line).
func writeSSHConfigEntry(island, block string) (status, path string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, "config")
	existing, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(existing), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "Host" && f[1] == "dejima-"+island {
			return "exists", path, nil
		}
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	defer fh.Close()
	sep := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n\n") {
		sep = "\n"
	}
	if _, err := fh.WriteString(sep + block); err != nil {
		return "", "", err
	}
	return "added", path, nil
}

// resolveSSHEndpoint asks the daemon for the SSH-façade addr and resolves a
// reachable host:port. enabled is false when the daemon has no --ssh listener.
// When the listener binds a wildcard/empty host (":2222"), we substitute a
// reachable host — the tailnet FQDN if Tailscale is up, else localhost.
func resolveSSHEndpoint(ctx context.Context) (host, port string, enabled bool, err error) {
	c, err := client()
	if err != nil {
		return "", "", false, err
	}
	o, err := c.Overview(ctx)
	if err != nil {
		return "", "", false, err
	}
	if o.SSHAddr == "" {
		return "", "", false, nil
	}
	h, p, addrErr := endpointFromAddr(o.SSHAddr, c.DaemonHost())
	return h, p, true, addrErr
}

// endpointFromAddr resolves a daemon-reported ssh listen addr into a reachable
// host:port. When the listener binds a wildcard/empty host (":2222"), the host
// is filled from (in order): the daemon host the client is talking to
// (daemonHost — correct for a remote client like GIZMO, where the SSH façade is
// on the *daemon's* host, not this machine), then this machine's tailnet FQDN
// (a local daemon), then localhost. Pure, so both the CLI and the TUI can use it.
func endpointFromAddr(sshAddr, daemonHost string) (host, port string, err error) {
	h, p, splitErr := net.SplitHostPort(sshAddr)
	if splitErr != nil {
		return "", "", fmt.Errorf("malformed ssh addr %q: %w", sshAddr, splitErr)
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		switch {
		case daemonHost != "":
			h = daemonHost
		default:
			h = reachableHost()
		}
	}
	return h, p, nil
}

// defaultPublicKey returns the path to the user's default SSH public key,
// preferring ed25519. Errors with guidance when none exists.
func defaultPublicKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		p := filepath.Join(home, ".ssh", name)
		if _, statErr := os.Stat(p); statErr == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no default SSH public key in ~/.ssh — generate one: ssh-keygen -t ed25519")
}

// reachableHost returns the best address a remote editor can dial: the tailnet
// FQDN when available (works cross-device), otherwise localhost (same-host use).
func reachableHost() string {
	if fqdn := sshTailnetFQDN(); fqdn != "" {
		return fqdn
	}
	return "localhost"
}

// sshTailnetFQDN returns this host's tailnet DNS name, or "" if Tailscale isn't
// present/up.
func sshTailnetFQDN() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if json.Unmarshal(out, &st) != nil {
		return ""
	}
	return strings.TrimSuffix(st.Self.DNSName, ".")
}

// printConnectHint prints a ready-to-run connect line, resolving the real
// address when the listener is up and falling back to a placeholder + how to
// enable it otherwise.
func printConnectHint(ctx context.Context, island string) {
	host, port, enabled, err := resolveSSHEndpoint(ctx)
	if err == nil && enabled {
		fmt.Printf("connect:  ssh %s@%s -p %s\n", island, host, port)
		fmt.Printf("VS Code / Cursor:  dejima ssh config %s --install\n", island)
		return
	}
	fmt.Printf("connect:  ssh %s@<daemon-host> -p <ssh-port>  (enable the listener: dejimad --ssh <addr>)\n", island)
}

// maybeAuthorizeAccountKey offers, during `service install --ssh`, to register
// this machine's default SSH key fleet-wide so every island — current and
// future — accepts it, the dead-simple "set up once" path. Interactive and
// opt-in: we never grab a key silently. AddAccountKey dedups, so re-running is
// harmless.
func maybeAuthorizeAccountKey() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	pub, err := defaultPublicKey()
	if err != nil {
		fmt.Println("  authorize a key for all islands later: dejima ssh authorize --account --key <pub>")
		return
	}
	fmt.Printf("Authorize this machine's key (%s) for ALL islands so VS Code/Cursor can connect? [Y/n]: ", pub)
	var input string
	_, _ = fmt.Scanln(&input)
	if t := strings.TrimSpace(input); t != "" && !strings.EqualFold(t, "y") {
		fmt.Println("  skipped — later: dejima ssh authorize --account --key " + pub)
		return
	}
	line, err := os.ReadFile(pub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  could not read %s: %v\n", pub, err)
		return
	}
	fp, err := sshfacade.AddAccountKey(strings.TrimSpace(string(line)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  could not authorize key: %v\n", err)
		return
	}
	fmt.Printf("  authorized %s for all islands. Set up your editor: dejima ssh config --all --install\n", fp)
}

// resolveKeyLine resolves the public-key line from (in priority) an explicit
// positional value, --key file, or stdin. positional is "" when none was given.
func resolveKeyLine(cmd *cobra.Command, positional, keyFile string) (string, error) {
	if positional != "" {
		return strings.TrimSpace(positional), nil
	}
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	b, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	if line == "" {
		return "", fmt.Errorf("no public key provided (pass it as an argument, --key <file>, or on stdin)")
	}
	return line, nil
}
