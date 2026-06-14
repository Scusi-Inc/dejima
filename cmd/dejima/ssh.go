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
	cmd.AddCommand(newSSHAuthorizeCmd(), newSSHListCmd(), newSSHRevokeCmd(), newSSHInfoCmd(), newSSHConfigCmd())
	return cmd
}

func newSSHAuthorizeCmd() *cobra.Command {
	var keyFile string
	cmd := &cobra.Command{
		Use:   "authorize <island> [public-key-line]",
		Short: "Authorize a public key to ssh into an island",
		Long: "Adds an OpenSSH public key to the island's authorized_keys. Provide the key as an\n" +
			"argument, via --key <file>, or on stdin:\n\n" +
			"  dejima ssh authorize myisland \"$(cat ~/.ssh/id_ed25519.pub)\"\n" +
			"  dejima ssh authorize myisland --key ~/.ssh/id_ed25519.pub\n" +
			"  cat ~/.ssh/id_ed25519.pub | dejima ssh authorize myisland",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			line, err := readKey(cmd, args, keyFile)
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
	return cmd
}

func newSSHListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <island>",
		Short: "List the public keys authorized to ssh into an island",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			keys, err := sshfacade.ListAuthorizedKeys(island)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Printf("no keys authorized for %q (ssh is closed until you add one)\n", island)
				return nil
			}
			for _, k := range keys {
				fmt.Printf("%s  %-12s %s\n", k.Fingerprint, k.Type, k.Comment)
			}
			return nil
		},
	}
}

func newSSHRevokeCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "revoke <island> [fingerprint]",
		Short: "Revoke a public key's SSH access to an island",
		Long: "Removes an authorized key by its SHA256 fingerprint (see `dejima ssh list`), or\n" +
			"--all to revoke every key (closes SSH to the island until you authorize a new one).",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			var (
				n   int
				err error
			)
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
	cmd.Flags().BoolVar(&all, "all", false, "revoke every authorized key for the island")
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
	var install bool
	cmd := &cobra.Command{
		Use:   "config <island>",
		Short: "Print or install an ~/.ssh/config entry for VS Code / Cursor Remote-SSH",
		Long: "Generates an ssh config Host block aliased `dejima-<island>`, resolving the real\n" +
			"connection address from the daemon (tailnet host when the listener binds a wildcard).\n" +
			"With --install it appends the block to ~/.ssh/config (idempotent); without it, prints\n" +
			"the block so you can review or redirect it. VS Code/Cursor then show `dejima-<island>`\n" +
			"in Remote-SSH: Connect to Host…",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			if _, err := project.Load(island); err != nil {
				return err
			}
			host, port, enabled, err := resolveSSHEndpoint(cmd.Context())
			if err != nil {
				return err
			}
			if !enabled {
				return fmt.Errorf("the SSH façade is not enabled on the daemon; start it with " +
					"`dejimad --ssh <addr>` (or `dejima service install --ssh :2222`)")
			}
			block := sshConfigBlock(island, host, port)
			if !install {
				fmt.Print(block)
				fmt.Fprintf(os.Stderr,
					"\n# add the above to ~/.ssh/config (or re-run with --install), then in VS Code/Cursor:\n"+
						"#   Remote-SSH: Connect to Host… → dejima-%s\n", island)
				return nil
			}
			return installSSHConfig(island, block)
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "append the entry to ~/.ssh/config (idempotent)")
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

// installSSHConfig appends the block to ~/.ssh/config unless an entry for this
// island is already present (so re-running is safe and won't duplicate).
func installSSHConfig(island, block string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config")
	existing, _ := os.ReadFile(path)
	marker := "Host dejima-" + island
	for _, line := range strings.Split(string(existing), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "Host" && f[1] == "dejima-"+island {
			fmt.Printf("~/.ssh/config already has %s (left unchanged)\n", marker)
			fmt.Printf("VS Code / Cursor → Remote-SSH: Connect to Host… → dejima-%s\n", island)
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	sep := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n\n") {
		sep = "\n"
	}
	if _, err := f.WriteString(sep + block); err != nil {
		return err
	}
	fmt.Printf("added %s to %s\n", marker, path)
	fmt.Printf("VS Code / Cursor → Remote-SSH: Connect to Host… → dejima-%s\n", island)
	return nil
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
	h, p, splitErr := net.SplitHostPort(o.SSHAddr)
	if splitErr != nil {
		return "", "", true, fmt.Errorf("daemon reported a malformed ssh addr %q: %w", o.SSHAddr, splitErr)
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = reachableHost()
	}
	return h, p, true, nil
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

// readKey resolves the public-key line from (in priority) the positional arg,
// --key file, or stdin.
func readKey(cmd *cobra.Command, args []string, keyFile string) (string, error) {
	if len(args) == 2 {
		return strings.TrimSpace(args[1]), nil
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
