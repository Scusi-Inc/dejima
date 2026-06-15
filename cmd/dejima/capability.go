package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// --- cap (capability broker) ----------------------------------------------
//
// Phase 1: grant / revoke / list the named host capabilities an island may
// invoke. Execution (`POST /v1/capabilities/execute`) and the macOS-Shortcut /
// Linux-script adapters land in later phases; today a grant is a recorded,
// ledgered permission with no runtime effect. See docs/capability-broker-spec.md.

func newCapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cap",
		Aliases: []string{"capability"},
		Short:   "Grant an island brokered access to host capabilities (Shortcuts/scripts).",
		Long: "The capability broker lets a contained brain invoke a curated host action — a " +
			"macOS Shortcut, or an executable in ~/.dejima/capabilities/ — by name, never a " +
			"shell. Access is deny-all by default; `cap grant` opens a single named target. " +
			"Every grant/invocation is recorded in the append-only Ledger. See " +
			"docs/capability-broker-spec.md.",
	}
	cmd.AddCommand(newCapGrantCmd(), newCapListCmd(), newCapRevokeCmd())
	return cmd
}

func newCapGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <island> <target>",
		Short: "Grant an island permission to invoke a named host capability.",
		Long: "Grants <island> permission to invoke <target> — a macOS Shortcut name, or a " +
			"script basename in ~/.dejima/capabilities/ on Linux. The target need not exist " +
			"yet; an invocation fails closed until it does.\n\n  dejima cap grant oc-home MorningBrief",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, target := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			grant, err := c.GrantCapability(cmd.Context(), island, target)
			if err != nil {
				return err
			}
			fmt.Printf("granted capability %q on %s\n", grant.Target, island)
			return nil
		},
	}
}

func newCapListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <island>",
		Aliases: []string{"ls"},
		Short:   "List an island's granted capabilities.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.ListCapabilityGrants(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(resp.Grants) == 0 {
				fmt.Printf("%s has no capability grants (deny-all: no host actions reachable)\n", args[0])
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TARGET\tGRANTED")
			for _, g := range resp.Grants {
				fmt.Fprintf(tw, "%s\t%s\n", g.Target, g.GrantedAt.Local().Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
}

func newCapRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <island> <target>",
		Short: "Revoke a capability grant.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, target := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RevokeCapability(cmd.Context(), island, target); err != nil {
				return err
			}
			fmt.Printf("revoked capability %q on %s\n", target, island)
			return nil
		},
	}
}
