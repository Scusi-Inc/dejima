package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/selfupdate"
)

// newUpdateCmd checks for a newer Dejima release and explains how to update.
// Self-applying the update (git pull+rebuild in source mode; download+replace in
// release mode) is a later, separately-reviewed slice; today this reports and
// hands off to the manual steps for the detected install mode.
func newUpdateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer Dejima release and how to update",
		Long: "Detects how this Dejima was installed (a source checkout vs a released binary) and\n" +
			"reports whether a newer release is available. Applying the update automatically is\n" +
			"not wired yet; for now it prints the steps for your install mode.",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := selfupdate.Check(cmd.Context())
			if err != nil {
				return fmt.Errorf("check for updates: %w", err)
			}
			fmt.Printf("current:  %s\n", st.Current)
			fmt.Printf("latest:   %s\n", st.Latest)
			fmt.Printf("mode:     %s\n", st.Mode)
			if !st.UpdateAvailable {
				fmt.Println("you're up to date.")
				return nil
			}
			fmt.Printf("\nan update is available (%s → %s).\n", st.Current, st.Latest)
			if checkOnly {
				return nil
			}
			fmt.Println("self-update isn't wired yet; update with:")
			fmt.Printf("  %s\n", selfupdate.ManualSteps(st.Mode))
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether an update is available; don't print update steps")
	return cmd
}
