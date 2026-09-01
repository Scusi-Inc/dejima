package main

import (
	"context"
	"fmt"
	"io"
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
	var daemonToo bool
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
				reportDaemonVersion(cmd.Context(), cmd.OutOrStdout(), daemonToo)
				return nil
			}
			dir := source
			if dir == "" {
				dir, err = resolveUpdateCheckout()
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
	cmd.Flags().BoolVar(&daemonToo, "daemon", false, "also update the daemon this client is pointed at (restarts it; attached terminals reconnect)")
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

// resolveUpdateCheckout finds the checkout to update: the one containing the
// current directory, else the one this install was BUILT FROM.
//
// It used to search upward from cwd and nothing else, so `dejima update` run
// from anywhere but inside the repo failed with "run this from your checkout" —
// which asks the operator for a path the daemon already knows. `dejima service
// install` records SourceDir precisely so self-update does not have to ask, and
// ResolveSourceDir has read it back since the day it was added. The update
// command simply never called it.
//
// cwd wins when it resolves, because an operator standing in a checkout means
// that one; the recorded dir is the fallback, not an override.
func resolveUpdateCheckout() (string, error) {
	cwd, _ := os.Getwd()
	if dir, err := selfupdate.FindCheckout(cwd); err == nil {
		return dir, nil
	}
	if dir := selfupdate.ResolveSourceDir(); dir != "" {
		fmt.Printf("using the checkout this install was built from: %s\n", dir)
		return dir, nil
	}
	return "", fmt.Errorf(
		"no dejima checkout found from %s, and this install recorded none.\n"+
			"Pass one explicitly:  dejima update --source <dir>", cwd)
}

// reportDaemonVersion closes the gap that made `dejima update` misleading.
//
// The command updates THIS BINARY and prints "done". On a machine that also
// hosts the daemon — a Windows box whose daemon lives in WSL, most obviously —
// that leaves the daemon on the old version while the command reports success.
// The operator did exactly this, saw v0.8.89, and found the daemon still on
// v0.8.87 with the TUI banner still asking to update it.
//
// It was not even fixable from the CLI: DaemonUpdate had one caller in the
// whole tree, the TUI's confirm dialog. So a broken confirm (or a headless
// machine) left no route at all.
//
// Reporting rather than silently applying by default is deliberate: updating
// the daemon RESTARTS it, and a restart is not something a routine `dejima
// update` should do to someone without saying so. --daemon opts in.
func reportDaemonVersion(ctx context.Context, w io.Writer, apply bool) {
	c, err := client()
	if err != nil {
		return // no daemon configured; nothing to say
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	st, err := c.DaemonUpdate(ctx, false, false)
	if err != nil {
		// Never turn a successful client update into a failure because the
		// daemon could not be reached. Say what is unknown and stop.
		fmt.Fprintf(w, "\nthe daemon's version couldn't be checked (%v)\n", err)
		fmt.Fprintln(w, "check it yourself with: dejima update --daemon")
		return
	}
	if !st.UpdateAvailable {
		fmt.Fprintf(w, "daemon:   %s (up to date)\n", st.Current)
		return
	}

	if !apply {
		fmt.Fprintf(w, "\nTHE DAEMON IS STILL ON %s (latest %s).\n", st.Current, st.Latest)
		fmt.Fprintln(w, "Updating this client did not update it — they are separate programs,")
		fmt.Fprintln(w, "and on Windows the daemon lives inside WSL rather than on this machine.")
		fmt.Fprintln(w, "\n  dejima update --daemon      # restarts the daemon; terminals reconnect")
		return
	}

	fmt.Fprintf(w, "\nupdating the daemon (%s → %s)…\n", st.Current, st.Latest)
	res, err := c.DaemonUpdate(ctx, true, false)
	if err != nil {
		fmt.Fprintf(w, "daemon update failed: %v\n", err)
		return
	}
	// Deferred is not a failure and must not read as one: the daemon refused
	// because terminals are attached, and the operator chooses when to drop them.
	if res.Deferred {
		fmt.Fprintf(w, "deferred: %d terminal(s) are attached, so the daemon was not restarted.\n", res.AttachedClients)
		fmt.Fprintln(w, "re-run with --force once they're closed, or detach them first.")
		return
	}
	fmt.Fprintf(w, "daemon updating to %s — it restarts itself; reattach in a moment.\n", res.Latest)
}
