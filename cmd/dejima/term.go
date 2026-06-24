package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// newTermCmd is the CLI surface for host terminals — uncontained operator shells
// running in tmux on the daemon host. It mirrors the TUI's "Host · not
// contained" section: list, create, attach, rename, remove. Every subcommand is
// a thin client of the same gated API the TUI uses, so the daemon's
// --host-terminals capability and operator-only auth (island tokens get 403)
// apply identically — the CLI widens convenience, not the security boundary.
func newTermCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "term",
		Short: "Manage host terminals (uncontained operator shells on the daemon host).",
		Long: "Host terminals are tmux sessions running on the DAEMON HOST itself — not in a " +
			"container. They are uncontained and operator-only: the daemon must be started with " +
			"--host-terminals, and island tokens can't reach them.\n\n" +
			"This is the CLI equivalent of the TUI's host-terminal section. `dejima term` with no " +
			"subcommand lists them.",
		// Bare `dejima term` lists, matching `dejima ls`'s convenience.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTermLs(cmd)
		},
	}
	cmd.AddCommand(newTermLsCmd(), newTermNewCmd(), newTermAttachCmd(), newTermRmCmd(), newTermRelabelCmd())
	return cmd
}

func newTermLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Short:   "List host terminals.",
		Aliases: []string{"list"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTermLs(cmd)
		},
	}
}

func runTermLs(cmd *cobra.Command) error {
	c, err := client()
	if err != nil {
		return err
	}
	terms, err := c.ListTerminals(cmd.Context())
	if err != nil {
		return err
	}
	if len(terms) == 0 {
		fmt.Println("no host terminals — `dejima term new` to open one")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tCREATED\tTMUX")
	for _, t := range terms {
		label := t.Label
		if label == "" {
			label = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s ago\t%s\n", t.ID, label, humanDuration(time.Since(t.CreatedAt)), t.Tmux())
	}
	return tw.Flush()
}

func newTermNewCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:     "new",
		Short:   "Open a new host terminal.",
		Aliases: []string{"create"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			t, err := c.CreateTerminal(cmd.Context(), label)
			if err != nil {
				return err
			}
			name := t.Label
			if name == "" {
				name = t.ID
			}
			fmt.Printf("created host terminal %s (%s)\n", t.ID, name)
			fmt.Printf("attach: dejima term attach %s\n", t.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "optional label shown in listings")
	return cmd
}

func newTermAttachCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "attach <id>",
		Short: "Attach to a host terminal's shell.",
		Long: "Open an interactive PTY into the host terminal's tmux session. Multiple clients " +
			"can attach at once (shared tmux). Detach with Ctrl-b then d; the session keeps " +
			"running on the host.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if label == "" {
				label = defaultLabel()
			}
			// Bare CLI attach — no dashboard to summon back to.
			return runTerminalSession(cmd.Context(), c, args[0], label, false)
		},
	}
	cmd.Flags().StringVar(&label, "as", "", "client label shown in presence (default: $HOSTNAME or 'cli')")
	return cmd
}

func newTermRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <id>",
		Short:   "Remove a host terminal (kills its tmux session).",
		Aliases: []string{"kill", "delete"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.DeleteTerminal(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("removed host terminal %s\n", args[0])
			return nil
		},
	}
}

func newTermRelabelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "relabel <id> <label>",
		Short: "Rename a host terminal.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			t, err := c.RelabelTerminal(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("renamed %s → %s\n", t.ID, t.Label)
			return nil
		},
	}
}
