package main

import (
	"context"
	"fmt"
	"os"
	"time"

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
	var force bool
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
			"    diverged tree is refused. Because this RESTARTS the daemon and closes every\n" +
			"    attached terminal fleet-wide, it is refused while terminals are attached\n" +
			"    unless you pass --force (containers and agents keep running; you reattach).\n\n" +
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
			// The source apply ends in `dejima service restart`, which RESTARTS the
			// daemon and drops EVERY attached terminal fleet-wide (containers and
			// agents keep running; clients reattach). Refuse without --force when
			// terminals are attached, so a routine `dejima update` never yanks live
			// sessions out from under an operator — the TUI enforces the same gate.
			if !force {
				if n, known := attachedClientCount(cmd.Context()); known && n > 0 {
					return fmt.Errorf(
						"%s attached — a source update RESTARTS the daemon and closes ALL open terminals fleet-wide "+
							"(containers and agents keep running; you just reattach). Detach them first, or re-run with --force",
						pluralClients(n))
				} else if !known {
					fmt.Println("\n⚠ could not verify attached terminals — the daemon restart will close any open " +
						"terminals fleet-wide (containers and agents keep running; you just reattach).")
				}
			}
			fmt.Printf("\napplying source update in %s:\n", dir)
			return selfupdate.ApplySource(cmd.Context(), dir, true, cmd.OutOrStdout(), selfupdate.ExecRunner(cmd.OutOrStdout()))
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether an update is available; don't apply it")
	cmd.Flags().StringVar(&source, "source", "", "path to the dejima checkout (default: found from the current directory)")
	cmd.Flags().BoolVar(&force, "force", false, "apply a daemon-restarting (source) update even while terminals are attached — closes them fleet-wide")
	return cmd
}

// attachedClientCount best-effort queries the local daemon for how many clients
// are currently attached across all islands. known=false means the count could
// not be obtained (daemon unreachable / older daemon / overview error), in which
// case the caller warns rather than hard-blocking. Used to gate the
// daemon-restarting source update.
func attachedClientCount(ctx context.Context) (n int, known bool) {
	c, err := client()
	if err != nil {
		return 0, false
	}
	octx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	o, err := c.Overview(octx)
	if err != nil || o == nil {
		return 0, false
	}
	return o.AttachedClients, true
}

// pluralClients renders an attached-client count for the refusal message.
func pluralClients(n int) string {
	if n == 1 {
		return "1 terminal is"
	}
	return fmt.Sprintf("%d terminals are", n)
}

// selfExe returns the running binary's path for display, or "this binary".
func selfExe() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "this binary"
}
