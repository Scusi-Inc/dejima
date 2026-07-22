package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/aoos/dejima/internal/api"
	"github.com/spf13/cobra"
)

// newMsgCmd is the intra-island agent mailbox CLI (Lane 5, Phase 1): same-island
// agents send/poll small typed messages through the daemon. Inside an island the
// island + sender default from the injected DEJIMA_PROJECT_NAME / DEJIMA_AGENT_ID,
// so an agent just runs `dejima msg send "…"`.
func newMsgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "msg",
		Short: "Send/receive intra-island agent messages (same-island only).",
	}
	cmd.AddCommand(newMsgSendCmd(), newMsgPollCmd())
	return cmd
}

func newMsgSendCmd() *cobra.Command {
	var island, from, to, topic string
	cmd := &cobra.Command{
		Use:   "send <payload>",
		Short: "Send a message to the island mailbox (to an agent, or broadcast).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island = orEnv(island, "DEJIMA_PROJECT_NAME")
			if island == "" {
				return fmt.Errorf("no island: pass --island (or run inside an island where DEJIMA_PROJECT_NAME is set)")
			}
			c, err := client()
			if err != nil {
				return err
			}
			m, err := c.SendMailbox(cmd.Context(), island, api.MailboxSendRequest{
				From:    orEnv(from, "DEJIMA_AGENT_ID"),
				To:      to,
				Topic:   topic,
				Payload: args[0],
			})
			if err != nil {
				return err
			}
			dest := "(broadcast)"
			if m.To != "" {
				dest = "→ " + m.To
			}
			fmt.Printf("sent #%d %s\n", m.Seq, dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&island, "island", "", "island name (default: $DEJIMA_PROJECT_NAME inside an island)")
	cmd.Flags().StringVar(&from, "from", "", "sender agent id (default: $DEJIMA_AGENT_ID)")
	cmd.Flags().StringVar(&to, "to", "", "recipient agent id (default: broadcast to the island)")
	cmd.Flags().StringVar(&topic, "topic", "", "optional topic/channel within the island")
	return cmd
}

func newMsgPollCmd() *cobra.Command {
	var island, agent string
	var since int64
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Read messages addressed to you (and broadcasts) since a cursor.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			island = orEnv(island, "DEJIMA_PROJECT_NAME")
			if island == "" {
				return fmt.Errorf("no island: pass --island (or run inside an island)")
			}
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.PollMailbox(cmd.Context(), island, orEnv(agent, "DEJIMA_AGENT_ID"), since)
			if err != nil {
				return err
			}
			if len(resp.Messages) == 0 {
				fmt.Printf("no new messages (cursor: %d)\n", resp.Latest)
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SEQ\tFROM\tTO\tTOPIC\tPAYLOAD")
			for _, m := range resp.Messages {
				to := m.To
				if to == "" {
					to = "(all)"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", m.Seq, m.From, to, m.Topic, m.Payload)
			}
			_ = tw.Flush()
			fmt.Printf("cursor: %d  (poll again with --since %d)\n", resp.Latest, resp.Latest)
			return nil
		},
	}
	cmd.Flags().StringVar(&island, "island", "", "island name (default: $DEJIMA_PROJECT_NAME inside an island)")
	cmd.Flags().StringVar(&agent, "agent", "", "your agent id (default: $DEJIMA_AGENT_ID); filters messages addressed to you")
	cmd.Flags().Int64Var(&since, "since", 0, "only messages with seq greater than this cursor")
	return cmd
}

// orEnv returns v if non-empty, else the value of environment variable key.
func orEnv(v, key string) string {
	if v != "" {
		return v
	}
	return os.Getenv(key)
}
