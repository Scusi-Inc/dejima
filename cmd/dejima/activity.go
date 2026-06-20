package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// newActivityCmd is the team activity feed: a curated, human-readable timeline of
// "who launched what, and which agent did what," built from the operational audit
// log + island ownership. It's the friendly companion to `dejima audit` (the raw,
// verifiable ledger): same source, team-facing rendering.
func newActivityCmd() *cobra.Command {
	var (
		actor    string
		island   string
		owner    string
		kind     string
		decision string
		since    string
		until    string
		limit    int
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "activity",
		Short: "Show the team activity feed — who launched what, which agent did what.",
		Long: "A curated, newest-first timeline over the audit log: island lifecycle and account\n" +
			"actions attributed to who+role, agent↔host access (Port/capability/MCP), and system\n" +
			"events — each enriched with the island's owner. Filter with --actor/--island/--owner/\n" +
			"--kind/--decision/--since/--until (AND-combined). --decision denied surfaces refused\n" +
			"attempts (a viewer blocked from a purge, an agent blocked from an ungranted server).\n\n" +
			"The full who-did-what timeline needs the daemon's operational audit log (dejimad\n" +
			"--audit); without it the feed still shows the always-on agent↔host broker records.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.Activity(cmd.Context(), api.ActivityFilter{
				Actor:    actor,
				Island:   island,
				Owner:    owner,
				Kind:     kind,
				Decision: decision,
				Since:    since,
				Until:    until,
				Limit:    limit,
			})
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp.Items)
			}
			if len(resp.Items) == 0 {
				fmt.Println("no activity yet")
				if !resp.AuditEnabled {
					fmt.Fprintln(os.Stderr, "note: the daemon's operational audit log is off — start dejimad with --audit for the full who-did-what feed.")
				}
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tACTOR\tROLE\tISLAND\tOWNER\tACTIVITY")
			for _, it := range resp.Items {
				summary := it.Summary
				if it.Decision == "denied" {
					summary = "✗ " + summary
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					it.Time.Local().Format("2006-01-02 15:04:05"),
					dash(it.Actor), dash(it.Role), dash(it.Island), dash(it.Owner), summary)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if !resp.AuditEnabled {
				fmt.Fprintln(os.Stderr, "\nnote: operational audit log is off (dejimad --audit) — showing agent↔host broker activity only.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "", "only activity by this actor (a token label, operator, or island:<name>)")
	cmd.Flags().StringVar(&island, "island", "", "only activity on this island")
	cmd.Flags().StringVar(&owner, "owner", "", "only activity on islands with this owner label")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind: lifecycle, broker, or system")
	cmd.Flags().StringVar(&decision, "decision", "", "only allowed or denied activity")
	cmd.Flags().StringVar(&since, "since", "", "only activity at or after this RFC3339 time")
	cmd.Flags().StringVar(&until, "until", "", "only activity at or before this RFC3339 time")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "show at most N items (newest first; 0 = all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the feed as JSON")
	return cmd
}

// dash renders an empty cell as "-" so columns stay readable.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
