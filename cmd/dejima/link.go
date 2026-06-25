package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/aoos/dejima/internal/api"
	"github.com/spf13/cobra"
)

// newLinkCmd manages inter-island info channels (Lane 5, Phase 2). Cross-island
// is deny-all: a channel exists only as an explicit, operator-granted,
// directional A→B grant on a named topic. grant/revoke/ls are operator-only;
// send is the in-island agent path (an island acts only as itself). There is no
// `link inbox` — a cross-island message lands in the recipient agent's ordinary
// mailbox, read with `dejima msg poll` (its Origin marks it cross-island).
func newLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Manage brokered inter-island info channels (deny-all).",
	}
	cmd.AddCommand(
		newLinkGrantCmd(), newLinkRevokeCmd(), newLinkLsCmd(), newLinkSendCmd(),
		newLinkExposeCmd(), newLinkUnexposeCmd(), newLinkExposedCmd(),
		newLinkActionCmd(), newLinkApprovalsCmd(), newLinkApproveCmd(), newLinkDenyCmd(),
	)
	return cmd
}

func newLinkGrantCmd() *cobra.Command {
	var topic string
	cmd := &cobra.Command{
		Use:   "grant <from-island> <to-island> --topic <t>",
		Short: "Authorize a directional info channel from→to on a topic (operator).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			c, err := client()
			if err != nil {
				return err
			}
			g, err := c.GrantLink(cmd.Context(), api.LinkGrantRequest{From: args[0], To: args[1], Topic: topic})
			if err != nil {
				return err
			}
			fmt.Printf("granted %s → %s on topic %q\n", g.From, g.To, g.Topic)
			return nil
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "named channel/topic")
	return cmd
}

func newLinkRevokeCmd() *cobra.Command {
	var topic string
	cmd := &cobra.Command{
		Use:   "revoke <from-island> <to-island> --topic <t>",
		Short: "Revoke a link grant (operator) — severs that reach immediately.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RevokeLink(cmd.Context(), args[0], args[1], topic); err != nil {
				return err
			}
			fmt.Printf("revoked %s → %s on topic %q\n", args[0], args[1], topic)
			return nil
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "named channel/topic")
	return cmd
}

func newLinkLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List inter-island link grants.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			grants, err := c.ListLinks(cmd.Context())
			if err != nil {
				return err
			}
			if len(grants) == 0 {
				fmt.Println("no link grants — cross-island is deny-all (grant one with `dejima link grant`)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "FROM\tTO\tTOPIC")
			for _, g := range grants {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", g.From, g.To, g.Topic)
			}
			return tw.Flush()
		},
	}
}

func newLinkSendCmd() *cobra.Command {
	var fromIsland, fromAgent, topic string
	cmd := &cobra.Command{
		Use:   "send <to-island> <to-agent> <payload> --topic <t>",
		Short: "Send an info message to a specific agent in another island (granted channel).",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromIsland == "" {
				fromIsland = os.Getenv("DEJIMA_PROJECT_NAME")
			}
			if fromIsland == "" {
				return fmt.Errorf("no sender island: pass --from (or run inside an island)")
			}
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			if fromAgent == "" {
				fromAgent = os.Getenv("DEJIMA_AGENT_ID")
			}
			c, err := client()
			if err != nil {
				return err
			}
			m, err := c.SendLink(cmd.Context(), fromIsland, api.LinkSendRequest{
				To: args[0], ToAgent: args[1], Payload: args[2], Topic: topic, FromAgent: fromAgent,
			})
			if err != nil {
				return err
			}
			fmt.Printf("delivered %s → %s/%s (#%d) on topic %q\n", fromIsland, m.Island, m.To, m.Seq, m.Topic)
			return nil
		},
	}
	cmd.Flags().StringVar(&fromIsland, "from", "", "sender island (default: $DEJIMA_PROJECT_NAME inside an island)")
	cmd.Flags().StringVar(&fromAgent, "from-agent", "", "sending agent id (default: $DEJIMA_AGENT_ID)")
	cmd.Flags().StringVar(&topic, "topic", "", "the granted topic")
	return cmd
}

func newLinkExposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expose <island> <action>",
		Short: "Expose a named action type this island accepts via delegation (operator).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			actions, err := c.ExposeAction(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("%s now exposes: %s\n", args[0], strings.Join(actions, ", "))
			return nil
		},
	}
}

func newLinkUnexposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unexpose <island> <action>",
		Short: "Stop exposing an action type (operator).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.UnexposeAction(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("%s no longer exposes %q\n", args[0], args[1])
			return nil
		},
	}
}

func newLinkExposedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exposed <island>",
		Short: "List the action types an island exposes.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			actions, err := c.ListExposedActions(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(actions) == 0 {
				fmt.Printf("%s exposes no actions (cross-island action delegation is deny-all)\n", args[0])
				return nil
			}
			fmt.Println(strings.Join(actions, "\n"))
			return nil
		},
	}
}

func newLinkActionCmd() *cobra.Command {
	var fromIsland, fromAgent, topic, params string
	cmd := &cobra.Command{
		Use:   "action <to-island> <to-agent> <action> --topic <t>",
		Short: "Request a named action on another island (pre-authorized runs; else queued).",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromIsland == "" {
				fromIsland = os.Getenv("DEJIMA_PROJECT_NAME")
			}
			if fromIsland == "" {
				return fmt.Errorf("no sender island: pass --from (or run inside an island)")
			}
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			if fromAgent == "" {
				fromAgent = os.Getenv("DEJIMA_AGENT_ID")
			}
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.RequestLinkAction(cmd.Context(), fromIsland, api.LinkActionRequest{
				To: args[0], ToAgent: args[1], Action: args[2], Topic: topic, Params: params, FromAgent: fromAgent,
			})
			if err != nil {
				return err
			}
			if resp.Status == "pending" {
				fmt.Printf("queued for operator approval (id %s)\n", resp.Pending)
			} else {
				fmt.Printf("action %q delivered to %s/%s\n", args[2], args[0], args[1])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromIsland, "from", "", "requesting island (default: $DEJIMA_PROJECT_NAME)")
	cmd.Flags().StringVar(&fromAgent, "from-agent", "", "requesting agent id (default: $DEJIMA_AGENT_ID)")
	cmd.Flags().StringVar(&topic, "topic", "", "the granted topic")
	cmd.Flags().StringVar(&params, "params", "", "optional typed params for the action")
	return cmd
}

func newLinkApprovalsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approvals",
		Short: "List cross-island action requests awaiting approval (operator).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			pending, err := c.ListPendingActions(cmd.Context())
			if err != nil {
				return err
			}
			if len(pending) == 0 {
				fmt.Println("no pending action approvals")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tFROM\tTO\tACTION\tTOPIC")
			for _, p := range pending {
				fmt.Fprintf(tw, "%s\t%s/%s\t%s/%s\t%s\t%s\n", p.ID, p.From, p.FromAgent, p.To, p.ToAgent, p.Action, p.Topic)
			}
			return tw.Flush()
		},
	}
}

func newLinkApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve and execute a pending action (operator).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.ApproveAction(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("approved + executed %s\n", args[0])
			return nil
		},
	}
}

func newLinkDenyCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "deny <id>",
		Short: "Deny a pending action (operator).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.DenyAction(cmd.Context(), args[0], reason); err != nil {
				return err
			}
			fmt.Printf("denied %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "optional reason recorded in the ledger")
	return cmd
}
