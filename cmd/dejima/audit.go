package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/spf13/cobra"
)

// --- audit ----------------------------------------------------------------

func newAuditCmd() *cobra.Command {
	var (
		verify   bool
		tail     int
		island   string
		typ      string
		actor    string
		decision string
		since    string
		until    string
		export   string
		out      string
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Read, filter, export, and verify the audit Ledger.",
		Long: "Reads the append-only, hash-chained Ledger: brokered Port scope grants/revokes " +
			"and file Trades, plus — when the daemon runs with --audit — operational API " +
			"requests and lifecycle events.\n\n" +
			"Filter with --island/--type/--actor/--decision/--since/--until (combined with AND); " +
			"--type matches a prefix (e.g. `--type port` matches port.grant and port.revoke). " +
			"--export jsonl|csv streams the filtered records for archival. `--verify` checks the " +
			"hash chain and exits non-zero if it's been tampered with — the tamper-evidence that " +
			"makes the record trustworthy. Verification always covers the WHOLE chain, regardless " +
			"of any filter.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			q := api.AuditQuery{
				Limit:    tail,
				Island:   island,
				Type:     typ,
				Actor:    actor,
				Decision: decision,
				Since:    since,
				Until:    until,
			}

			// Export path: stream the filtered records out verbatim.
			if export != "" {
				rc, err := c.AuditExport(cmd.Context(), q, export)
				if err != nil {
					return err
				}
				defer rc.Close()
				w := io.Writer(os.Stdout)
				if out != "" {
					f, err := os.Create(out)
					if err != nil {
						return err
					}
					defer f.Close()
					w = f
				}
				if _, err := io.Copy(w, rc); err != nil {
					return err
				}
				if out != "" {
					fmt.Fprintf(os.Stderr, "exported %s to %s\n", export, out)
				}
				return nil
			}

			resp, err := c.Audit(cmd.Context(), q)
			if err != nil {
				return err
			}
			if verify {
				if !resp.Verified {
					return fmt.Errorf("ledger verification FAILED: %s", resp.Error)
				}
				fmt.Printf("ledger OK — %d entries, hash chain intact\n", resp.Total)
				// `--verify` is the sentence someone quotes to a team lead, so it has to
				// say what it verified. The chain proves nobody edited these rows; it
				// says nothing about whether a self-reported one happened.
				if note := chainNote(resp.Entries); note != "" {
					fmt.Println(note)
					if n := provenanceNote(resp.Entries); n != "" {
						fmt.Println(n)
					}
				}
				// Verification covers the WHOLE chain regardless of filters; the
				// provenance scan above only saw what was returned. Without this line, a
				// filtered `--verify` that happens to return no self-reported rows prints
				// an unqualified "ledger OK" — silence reading as "and every row is
				// vouched for", which is the reassuring direction to be wrong in.
				if resp.Returned < resp.Total {
					fmt.Printf("(provenance assessed over the %d rows returned, not all %d)\n", resp.Returned, resp.Total)
				}
				return nil
			}
			if len(resp.Entries) == 0 {
				fmt.Println("no matching ledger entries")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			// The leading column carries a mark only for rows Dejima cannot vouch
			// for; an all-brokered ledger prints exactly as it did before. It leads
			// the row because it qualifies everything after it.
			fmt.Fprintln(tw, "\tSEQ\tWHEN\tTYPE\tISLAND\tACTOR\tDETAIL\tDECISION")
			for _, e := range resp.Entries {
				fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					provenanceMark(e.Provenance),
					e.Seq, e.Time.Local().Format("2006-01-02 15:04:05"),
					e.Type, e.Island, e.Actor, auditDetail(e), e.Decision)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if note := provenanceNote(resp.Entries); note != "" {
				fmt.Println(note)
			}
			if resp.Returned < resp.Total {
				fmt.Fprintf(os.Stderr, "\nshowing %d of %d entries (filtered)\n", resp.Returned, resp.Total)
			}
			if !resp.Verified {
				fmt.Fprintf(os.Stderr, "\nWARNING: ledger hash chain FAILED verification: %s\n", resp.Error)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "verify the hash chain; exit non-zero if it's been tampered with")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "show only the last N entries (0 = all)")
	cmd.Flags().StringVar(&island, "island", "", "only entries for this island")
	cmd.Flags().StringVar(&typ, "type", "", "only entries whose type matches this prefix (e.g. port, trade, api, island)")
	cmd.Flags().StringVar(&actor, "actor", "", "only entries by this actor")
	cmd.Flags().StringVar(&decision, "decision", "", "only allowed or denied entries")
	cmd.Flags().StringVar(&since, "since", "", "only entries at or after this RFC3339 time")
	cmd.Flags().StringVar(&until, "until", "", "only entries at or before this RFC3339 time")
	cmd.Flags().StringVar(&export, "export", "", "stream the filtered records instead of a table: jsonl or csv")
	cmd.Flags().StringVarP(&out, "out", "o", "", "with --export, write to this file instead of stdout")
	return cmd
}

// auditDetail renders the most informative field for a ledger entry's row,
// adapting to the record kind: operational requests show "METHOD path [status]",
// brokered records show the scope/path (and byte count for trades).
func auditDetail(e ledger.Entry) string {
	if e.Method != "" {
		d := e.Method + " " + e.Path
		if e.Status != 0 {
			d += fmt.Sprintf(" [%d]", e.Status)
		}
		return d
	}
	d := e.Scope
	if e.Path != "" {
		if d != "" {
			d += ":"
		}
		d += e.Path
	}
	if e.Bytes > 0 {
		d += fmt.Sprintf(" (%dB)", e.Bytes)
	}
	if d == "" {
		d = e.Detail
	}
	return d
}
