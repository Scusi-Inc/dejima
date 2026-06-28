package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// newSpawnCmd is the operator's control over agent-initiated EPHEMERAL sub-agents:
// off by default, granted with an explicit budget. Granting is operator-only — an
// in-island agent can spawn (within a grant) but can never grant. NOTE: today's
// grant covers CO-LOCATED sub-agents that SHARE the parent island's sandbox
// (workspace/FS/container) — not isolated from the parent; the isolated
// ephemeral-island variant is a later addition.
func newSpawnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spawn",
		Short: "Govern agent-initiated ephemeral sub-agents (operator).",
		Long: "Agents cannot spawn sub-agents by default. `spawn grant` opts an island in\n" +
			"with a budget (max concurrent/total, allowed types, per-sub-agent TTL and\n" +
			"memory/cpu caps); the agent then spawns within that budget, every spawn\n" +
			"ledgered. Granting is operator-only. Co-located sub-agents SHARE the parent\n" +
			"island's sandbox (not isolated from it).",
	}
	cmd.AddCommand(newSpawnGrantCmd(), newSpawnShowCmd(), newSpawnRevokeCmd())
	return cmd
}

func newSpawnGrantCmd() *cobra.Command {
	var req api.SpawnGrantRequest
	cmd := &cobra.Command{
		Use:   "grant <island>",
		Short: "Grant an island a budget to spawn ephemeral sub-agents.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if req.MaxConcurrent <= 0 {
				return fmt.Errorf("--max-concurrent must be > 0")
			}
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.SetSpawnGrant(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			printSpawnGrant(cmd, args[0], resp)
			return nil
		},
	}
	cmd.Flags().IntVar(&req.MaxConcurrent, "max-concurrent", 0, "max live sub-agents at once (required, > 0)")
	cmd.Flags().IntVar(&req.MaxTotal, "max-total", 0, "max lifetime spawns (0 = unlimited within the grant)")
	cmd.Flags().StringSliceVar(&req.Types, "types", nil, "allowed agent types (default: any spawnable type)")
	cmd.Flags().StringVar(&req.TTL, "ttl", "", "per-sub-agent max lifetime, e.g. 1h (0 = no time cap)")
	cmd.Flags().StringVar(&req.PerAgentMemory, "memory", "", "per-sub-agent memory cap, e.g. 512m")
	cmd.Flags().StringVar(&req.PerAgentCPUs, "cpus", "", "per-sub-agent cpu cap, e.g. 1.0")
	return cmd
}

func newSpawnShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <island>",
		Short: "Show an island's spawn grant (or that it has none).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.GetSpawnGrant(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printSpawnGrant(cmd, args[0], resp)
			return nil
		},
	}
}

func newSpawnRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <island>",
		Short: "Revoke an island's spawn grant (its agents can no longer spawn).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RevokeSpawnGrant(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked spawn grant for %s\n", args[0])
			return nil
		},
	}
}

func printSpawnGrant(cmd *cobra.Command, island string, resp *api.SpawnGrantResponse) {
	out := cmd.OutOrStdout()
	if resp == nil || !resp.Granted || resp.Grant == nil {
		fmt.Fprintf(out, "%s: no spawn grant — its agents cannot spawn sub-agents\n", island)
		return
	}
	g := resp.Grant
	fmt.Fprintf(out, "%s: spawn granted\n", island)
	fmt.Fprintf(out, "  max concurrent: %d\n", g.MaxConcurrent)
	if g.MaxTotal > 0 {
		fmt.Fprintf(out, "  max total:      %d\n", g.MaxTotal)
	}
	if len(g.Types) > 0 {
		fmt.Fprintf(out, "  types:          %v\n", g.Types)
	}
	if g.TTL > 0 {
		fmt.Fprintf(out, "  per-agent ttl:  %s\n", g.TTL)
	}
	if g.PerAgentMemory != "" || g.PerAgentCPUs != "" {
		fmt.Fprintf(out, "  per-agent caps: memory=%s cpus=%s\n", g.PerAgentMemory, g.PerAgentCPUs)
	}
}
