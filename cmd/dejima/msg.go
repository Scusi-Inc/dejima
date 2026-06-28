package main

import (
	"fmt"
	"os"
	"strings"
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
			// Permissive delivery: a directed `--to` that matches no agent in the
			// island roster is still delivered (you may address a handle that isn't
			// live yet, and the roster can be transiently empty), but the daemon
			// flags it so we warn the sender — to stderr, so it never corrupts the
			// "sent #N" line a script may parse on stdout. Each roster entry is
			// rendered with the shared agentDisplay helper, label-first to match
			// the names-primary house style (#198) everywhere else (ls/ledger);
			// the id stays visible since it (and the label) both still resolve.
			if m.UnknownRecipient {
				roster := make([]string, 0, len(m.Roster))
				for _, a := range m.Roster {
					roster = append(roster, agentDisplay(a.Label, a.ID))
				}
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: no agent %q in island roster (current: %s) — delivered anyway\n",
					m.To, strings.Join(roster, ", "))
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
			// Resolve same-island agent ids to their human labels so the operator
			// sees "backend (a1)" not a bare "a1". The roster (id→Label, set via
			// `dejima agent rename`) is the source d5 owns; we only read it here.
			// Best-effort: on error, fall back to bare ids.
			label := func(string) string { return "" }
			if agents, lerr := c.ListAgents(cmd.Context(), island); lerr == nil {
				labelOf := map[string]string{}
				for _, a := range agents {
					if a.Label != "" {
						labelOf[a.ID] = a.Label
					}
				}
				label = func(id string) string { return labelOf[id] }
			}
			// display renders an agent id name-first as "label (id)", falling back
			// to the bare id. Shared helper so every CLI surface reads identically.
			display := func(id string) string {
				return agentDisplay(label(id), id)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SEQ\tFROM\tTO\tTOPIC\tPAYLOAD")
			for _, m := range resp.Messages {
				to := "(all)"
				if m.To != "" {
					to = display(m.To)
				}
				// A cross-island message carries daemon-stamped provenance (Origin):
				// show the sender's NAME + island ("janus/planning") instead of a bare
				// id the recipient can't resolve. Same-island resolves the id to its
				// label via the local roster above.
				from := display(m.From)
				if m.Origin != nil {
					name := m.Origin.FromLabel
					if name == "" {
						name = m.From
					}
					from = m.Origin.SourceIsland + "/" + name
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", m.Seq, from, to, m.Topic, m.Payload)
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
