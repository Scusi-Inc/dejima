package main

import (
	"fmt"
	"io"
	"os"
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
			"`dejimad --ssh <addr>`. These subcommands manage the authorized keys host-side.",
	}
	cmd.AddCommand(newSSHAuthorizeCmd(), newSSHInfoCmd())
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
			fmt.Printf("connect:  ssh %s@<daemon-host> -p <ssh-port>\n", island)
			return nil
		},
	}
	cmd.Flags().StringVar(&keyFile, "key", "", "read the public key from this file")
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
			fmt.Printf("island:        %s\n", island)
			fmt.Printf("connect:       ssh %s@<daemon-host> -p <ssh-port>\n", island)
			fmt.Printf("host key:      %s\n", sshfacade.Fingerprint(signer))
			fmt.Printf("authorize a key:  dejima ssh authorize %s --key ~/.ssh/id_ed25519.pub\n", island)
			fmt.Println("(the daemon must be started with `dejimad --ssh <addr>`)")
			return nil
		},
	}
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
