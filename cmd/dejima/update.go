package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/selfupdate"
)

// newUpdateCmd checks for a newer Dejima release and explains how to update.
// Self-applying the update (git pull+rebuild in source mode; download+replace in
// release mode) is a later, separately-reviewed slice; today this reports and
// hands off to the manual steps for the detected install mode.
func newUpdateCmd() *cobra.Command {
	var checkOnly, apply, yes bool
	var source string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer Dejima release, or apply one (source installs)",
		Long: "Detects how this Dejima was installed (a source checkout vs a released binary) and\n" +
			"reports whether a newer release is available.\n\n" +
			"With --apply on a *source* install, it fast-forwards your checkout and reinstalls\n" +
			"(git pull --ff-only && make install && dejima service restart). --apply alone is a\n" +
			"dry run; add --yes to execute. A dirty or diverged tree is refused.\n\n" +
			"With --apply --yes on a *release* (binary) install, it downloads the latest release,\n" +
			"verifies it against the release SHA256SUMS, and replaces this binary in place.",
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

			if !apply {
				if checkOnly {
					return nil
				}
				fmt.Println("update with:")
				fmt.Printf("  %s\n", selfupdate.ManualSteps(st.Mode))
				fmt.Println("(or, on a source install: dejima update --apply)")
				return nil
			}

			// --apply path.
			if st.Mode == selfupdate.ModeRelease {
				// Release install: download the new binary, verify it against the
				// release SHA256SUMS, and replace this executable in place.
				if !yes {
					fmt.Printf("\nwould download %s and replace this binary (%s). re-run with --yes to apply.\n",
						st.Latest, selfExe())
					return nil
				}
				if err := selfupdate.ApplyReleaseSelf(cmd.Context(), st.Latest, cmd.OutOrStdout()); err != nil {
					return fmt.Errorf("apply update: %w", err)
				}
				fmt.Println("done — restart any running `dejima` to pick up the new version.")
				return nil
			}
			dir := source
			if dir == "" {
				cwd, _ := os.Getwd()
				dir, err = selfupdate.FindCheckout(cwd)
				if err != nil {
					return err
				}
			}
			fmt.Printf("\napplying source update in %s%s:\n", dir, dryRunSuffix(yes))
			return selfupdate.ApplySource(cmd.Context(), dir, yes, cmd.OutOrStdout(), selfupdate.ExecRunner(cmd.OutOrStdout()))
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether an update is available; don't print update steps")
	cmd.Flags().BoolVar(&apply, "apply", false, "apply the update (source installs); a dry run unless --yes is also given")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --apply, actually execute (otherwise it's a dry run)")
	cmd.Flags().StringVar(&source, "source", "", "path to the dejima checkout (default: found from the current directory)")
	return cmd
}

// selfExe returns the running binary's path for display, or "this binary".
func selfExe() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "this binary"
}

func dryRunSuffix(execute bool) string {
	if execute {
		return ""
	}
	return " (dry run)"
}
