package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// --- audit ----------------------------------------------------------------

func newAuditCmd() *cobra.Command {
	var verify bool
	var tail int
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show the brokered-operation Ledger (Port grants and file Trades).",
		Long: "Reads the append-only, hash-chained Ledger of every Port scope grant/revoke and " +
			"file Trade (intake/export). `--verify` checks the hash chain and exits non-zero if " +
			"it has been tampered with — the tamper-evidence that makes brokered file access " +
			"safe to grant.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.Audit(cmd.Context(), tail)
			if err != nil {
				return err
			}
			if verify {
				if !resp.Verified {
					return fmt.Errorf("ledger verification FAILED: %s", resp.Error)
				}
				fmt.Printf("ledger OK — %d entries, hash chain intact\n", resp.Total)
				return nil
			}
			if len(resp.Entries) == 0 {
				fmt.Println("ledger is empty (no brokered operations recorded)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SEQ\tWHEN\tTYPE\tISLAND\tSCOPE\tPATH\tBYTES\tDECISION")
			for _, e := range resp.Entries {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
					e.Seq, e.Time.Local().Format("2006-01-02 15:04:05"),
					e.Type, e.Island, e.Scope, e.Path, e.Bytes, e.Decision)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if !resp.Verified {
				fmt.Fprintf(os.Stderr, "\nWARNING: ledger hash chain FAILED verification: %s\n", resp.Error)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "verify the hash chain; exit non-zero if it's been tampered with")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "show only the last N entries (0 = all)")
	return cmd
}
