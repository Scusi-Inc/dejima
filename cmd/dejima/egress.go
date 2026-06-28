package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/egress"
)

// newEgressCmd is the operator interface to the island egress gate: see where an
// island connects out (observe), and set a per-island policy (observe/enforce +
// allow/deny). It's the CLI face of GET/PATCH /v1/islands/{name}/egress[/policy];
// the daemon must be started with --egress-proxy for any of it to take effect.
func newEgressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Observe and control an island's outbound network (operator).",
		Long: "See an island's outbound connections and set its egress policy.\n" +
			"Posture per island: observe (default — allow all + log) or enforce (deny-all\n" +
			"except the allow-list); a deny entry always wins; host match is exact-or-\n" +
			"subdomain (\"github.com\" covers \"api.github.com\"). Requires the daemon to run\n" +
			"with --egress-proxy. Enforcement is at the proxy (relies on HTTPS_PROXY\n" +
			"routing) — not yet airtight; a firewall-forced mode is a future follow-up.",
	}
	cmd.AddCommand(newEgressShowCmd(), newEgressPolicyCmd(), newEgressModeCmd(),
		newEgressAllowCmd(), newEgressDenyCmd(), newEgressRmCmd())
	return cmd
}

func newEgressShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <island>",
		Short: "Show an island's recent outbound connections (observed by the proxy).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.GetEgress(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(resp.Events) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no egress observed (the proxy may be disabled, or the island hasn't connected out)")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WHEN\tDECISION\tMETHOD\tDESTINATION")
			for _, e := range resp.Events {
				dest := e.Host
				if e.Port != "" {
					dest += ":" + e.Port
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Time.Format("15:04:05"), e.Decision, e.Method, dest)
			}
			return w.Flush()
		},
	}
}

// printPolicy renders an island's policy.
func printPolicy(cmd *cobra.Command, p *egress.IslandPolicy) {
	mode := p.Mode
	if mode == "" {
		mode = egress.ModeObserve
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mode:  %s\n", mode)
	fmt.Fprintf(cmd.OutOrStdout(), "allow: %v\n", p.Allow)
	fmt.Fprintf(cmd.OutOrStdout(), "deny:  %v\n", p.Deny)
}

func newEgressPolicyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "policy <island>",
		Short: "Show an island's egress policy.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			p, err := c.GetEgressPolicy(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printPolicy(cmd, p)
			return nil
		},
	}
}

// applyPolicy is the shared patch+print path for mode/allow/deny/rm.
func applyPolicy(cmd *cobra.Command, island string, patch egress.PolicyPatch) error {
	c, err := client()
	if err != nil {
		return err
	}
	p, err := c.PatchEgressPolicy(cmd.Context(), island, patch)
	if err != nil {
		return err
	}
	printPolicy(cmd, p)
	return nil
}

func newEgressModeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mode <island> <observe|enforce>",
		Short: "Set an island's egress posture (observe = allow all + log; enforce = deny-all except allow).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyPolicy(cmd, args[0], egress.PolicyPatch{Mode: egress.Mode(args[1])})
		},
	}
}

func newEgressAllowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "allow <island> <host>...",
		Short: "Allow outbound to host(s) (used in enforce mode; exact-or-subdomain).",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyPolicy(cmd, args[0], egress.PolicyPatch{AddAllow: args[1:]})
		},
	}
}

func newEgressDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny <island> <host>...",
		Short: "Always block outbound to host(s) (wins over allow, even in observe mode).",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyPolicy(cmd, args[0], egress.PolicyPatch{AddDeny: args[1:]})
		},
	}
}

func newEgressRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <island> <host>...",
		Short: "Remove host(s) from both the allow and deny lists.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts := args[1:]
			return applyPolicy(cmd, args[0], egress.PolicyPatch{RemoveAllow: hosts, RemoveDeny: hosts})
		},
	}
}
