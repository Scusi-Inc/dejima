package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/selfupdate"
)

// newUpdateCmd checks for a newer Dejima release and, by default, applies it.
// "update means update": the bare command upgrades in place — download + verify
// + atomic replace for a released binary, or git pull --ff-only + make install +
// service restart for a source checkout. `--check` is the look-don't-touch
// escape hatch. Integrity guards live in the selfupdate package (SHA256SUMS
// verification for releases; refusal of a dirty/diverged tree for source).
func newUpdateCmd() *cobra.Command {
	var checkOnly bool
	var source string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update Dejima to the latest release (use --check to only look)",
		Long: "Detects how this Dejima was installed (a source checkout vs a released binary) and\n" +
			"upgrades it to the latest release.\n\n" +
			"By default `dejima update` applies the update:\n" +
			"  • release (binary) install — downloads the latest release, verifies it against the\n" +
			"    release SHA256SUMS, and replaces this binary in place.\n" +
			"  • source install — fast-forwards your checkout and reinstalls\n" +
			"    (git pull --ff-only && make install && dejima service restart). A dirty or\n" +
			"    diverged tree is refused.\n\n" +
			"Use --check to only report whether an update is available, without applying it.",
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

			// --check: look, don't touch. Point at the one-word command to apply.
			if checkOnly {
				fmt.Println("\napply it with: dejima update")
				return nil
			}

			// Default path: apply the update.
			if st.Mode == selfupdate.ModeRelease {
				fmt.Printf("\ndownloading %s and replacing this binary (%s)…\n", st.Latest, selfExe())
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
			fmt.Printf("\napplying source update in %s:\n", dir)
			return selfupdate.ApplySource(cmd.Context(), dir, true, cmd.OutOrStdout(), selfupdate.ExecRunner(cmd.OutOrStdout()))
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether an update is available; don't apply it")
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
