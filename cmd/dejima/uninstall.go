package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/paths"
)

// islandAtRisk mirrors the daemon's purge guard so `dejima uninstall` can
// pre-flight the whole batch and refuse before purging anything. Returns a
// human reason when the island has at-risk work (or can't be verified), or ""
// when it's safe to purge.
func islandAtRisk(ctx context.Context, c *api.Client, isl api.IslandInfo) string {
	if isl.Container != "running" {
		if isl.NoRepo {
			// "unpushed work can't be verified" is a category error here, and the
			// remedy the caller is offered ("commit/push, or `dejima wake` to
			// verify") is an errand that cannot succeed: there is no git and no
			// remote, so waking it verifies nothing. Say what's actually true —
			// this is the one kind of island whose contents exist in exactly one
			// place — and point at a check that can actually be run.
			return "no repo, so nothing in it is backed up anywhere — start it and copy out anything you want to keep"
		}
		return "not running — unpushed work can't be verified"
	}
	d, err := c.GetIsland(ctx, isl.Name)
	if err != nil || d.Git == nil {
		return "" // no git info → nothing git-tracked to lose
	}
	var risks []string
	if !d.Git.Clean && d.Git.DirtyFiles > 0 {
		risks = append(risks, countNoun(d.Git.DirtyFiles, "uncommitted change"))
	}
	if d.Git.Ahead > 0 {
		risks = append(risks, countNoun(d.Git.Ahead, "unpushed commit"))
	}
	return strings.Join(risks, " and ")
}

// plainDaemonError turns a connection error into a phrase a non-engineer can
// act on. The raw string is written for whoever debugs the daemon; it surfaces
// here to someone who is uninstalling and, at this moment, mostly wants to know
// whether their work is safe. "invalid token" answers that question for nobody.
func plainDaemonError(raw string) string {
	switch {
	case strings.Contains(raw, "invalid token"), strings.Contains(raw, "unauthorized"), strings.Contains(raw, "401"):
		return "this Mac's saved sign-in for it is no longer valid"
	case strings.Contains(raw, "connection refused"), strings.Contains(raw, "no such file"):
		return "it isn't running on this Mac"
	case strings.Contains(raw, "timeout"), strings.Contains(raw, "deadline exceeded"), strings.Contains(raw, "no route to host"):
		return "this Mac can't reach it right now"
	default:
		return raw
	}
}

// daemonDownNotice closes out an uninstall that ran without a reachable daemon.
// The local teardown is done; what it could NOT do is island state, which lives
// in Docker. Naming the exact commands matters more here than anywhere else in
// the flow: the binary that knew how to enumerate islands has just been removed,
// so "check your islands" would be advice the operator can no longer act on.
func daemonDownNotice(purge bool) string {
	var b strings.Builder
	b.WriteString("\nDone — Dejima is removed from this Mac")
	if purge {
		b.WriteString(", along with its ~/.dejima settings")
	}
	b.WriteString(".\n\n")
	b.WriteString("Nothing of yours was deleted. Islands can only be changed by the Dejima\n")
	b.WriteString("service, and it wasn't answering, so any that exist are untouched — on\n")
	b.WriteString("whichever Mac runs them, which may not be this one.\n\n")
	b.WriteString("If islands DO live on this Mac, these two commands list what remains\n")
	b.WriteString("(the `dejima` command that could show you is the one just removed):\n\n")
	b.WriteString("  docker ps -a --filter name=dejima\n")
	b.WriteString("  docker volume ls | grep dejima\n\n")
	if purge {
		// Under --purge-all the operator asked for everything to go, and we
		// deleted the config that tracked these volumes — so surviving volumes
		// are now orphaned. Say that plainly instead of leaving them to find out.
		b.WriteString("Because ~/.dejima is gone, any surviving volumes are no longer tracked:\n")
		b.WriteString("a reinstall won't re-adopt them. Remove them by hand when you're sure\n")
		b.WriteString("nothing in them is unpushed (`docker volume rm <name>`), or leave them\n")
		b.WriteString("and reclaim the work from the volume directly.\n")
	} else {
		b.WriteString("Your settings in ~/.dejima were kept, so installing Dejima again on this\n")
		b.WriteString("Mac picks any of its islands back up.\n")
	}
	return b.String()
}

// uninstallMode is the destructive scope chosen by the operator. There is no
// default: bare `dejima uninstall` must refuse and force an explicit choice so
// nobody nukes their islands by reflex.
type uninstallMode int

const (
	uninstallModeUnset uninstallMode = iota
	// keepIslands removes the daemon, binaries, and live containers but KEEPS
	// the named volumes and ~/.dejima config, so a fresh install re-adopts the
	// pre-existing islands.
	uninstallModeKeepIslands
	// purgeAll is the full nuke: purge every island (volumes included) and
	// delete ~/.dejima.
	uninstallModePurgeAll
)

// resolveUninstallMode maps the (mutually exclusive) flags to a mode, or returns
// an error when the operator gave no choice or a contradictory one. `--keep-data`
// is a deprecated alias for `--keep-islands` (it used to keep config but still
// destroy volumes — a flag that lied about what it deleted).
func resolveUninstallMode(keepIslands, purgeAll, keepData bool) (uninstallMode, error) {
	keep := keepIslands || keepData
	switch {
	case keep && purgeAll:
		return uninstallModeUnset, errors.New("--keep-islands and --purge-all are mutually exclusive")
	case keep:
		return uninstallModeKeepIslands, nil
	case purgeAll:
		return uninstallModePurgeAll, nil
	default:
		return uninstallModeUnset, errors.New(
			"refusing to uninstall without an explicit choice — pick one:\n" +
				"  dejima uninstall --keep-islands   keep your islands' volumes + config; a reinstall re-adopts them\n" +
				"  dejima uninstall --purge-all      delete everything: islands, volumes, and ~/.dejima (irreversible)")
	}
}

func newUninstallCmd() *cobra.Command {
	var yes, force, systemSvc, keepIslands, purgeAll, keepData, clientOnly bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Dejima. Choose --keep-islands (re-adoptable) or --purge-all (irreversible).",
		Long: "Remove Dejima from this host. You must choose a scope — there is no\n" +
			"destructive default:\n\n" +
			"  --keep-islands  Remove the dejimad service, the dejima/dejimad binaries, and\n" +
			"                  the live island containers, but KEEP the named volumes and\n" +
			"                  ~/.dejima config. A later `curl … | sh` reinstall re-adopts the\n" +
			"                  pre-existing islands (volumes + config survive).\n\n" +
			"  --purge-all     Full removal: purge every island (deleting its volumes),\n" +
			"                  uninstall the service, remove the binaries, and delete ~/.dejima.\n" +
			"                  Destructive and irreversible.\n\n" +
			"Both honor the unpushed-work guard (use --force to bypass) and confirm first\n" +
			"unless --yes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --client is the no-daemon path: a laptop/Windows box that only drives
			// a remote server has nothing local to tear down. Handle it before any
			// daemon contact or mode resolution.
			if clientOnly {
				return uninstallClient(yes)
			}
			mode, err := resolveUninstallMode(keepIslands, purgeAll, keepData)
			if err != nil {
				return err
			}
			// Refuse before touching anything if this shell is inside the daemon
			// we're about to tear down — that combination deadlocks (see
			// preflightNotInsideDaemon), and it deadlocks AFTER the sudo prompt
			// and after islands have been deleted, which is the worst moment.
			if err := preflightNotInsideDaemon(force); err != nil {
				return err
			}
			if keepData {
				fmt.Fprintln(os.Stderr, "note: --keep-data is deprecated; it now means --keep-islands (keeps volumes + config so a reinstall re-adopts).")
			}

			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			// A daemon we can't reach used to abort the whole uninstall — which
			// meant a BROKEN install was the one thing you couldn't uninstall, at
			// exactly the moment you most want to start clean. Degrade instead.
			//
			// This is safe specifically because island volumes are only ever
			// deleted through the daemon: with it down we touch no island at all,
			// so the unpushed-work guard has nothing left to protect. What we do
			// tear down — the service, the binaries, ~/.dejima — is all local and
			// needs no daemon. Anything we couldn't reach is named at the end
			// rather than silently skipped.
			islands, err := c.ListIslands(ctx)
			daemonDown := ""
			if err != nil {
				daemonDown = err.Error()
				islands = nil
			}

			// Pre-flight the unpushed-work guard across ALL islands first, so we
			// never touch some and then abort on a guarded one (half-uninstall).
			// keep-islands preserves volumes, so unpushed work isn't lost — but
			// removing a live container still interrupts in-flight work, so we
			// guard both modes uniformly.
			if !force && daemonDown == "" {
				// This queries each island (a network round-trip apiece) before any
				// output — announce it so the terminal isn't blank while it runs.
				if len(islands) > 0 {
					fmt.Printf("Checking %s for unpushed work…\n", countNoun(len(islands), "island"))
				}
				var atRisk []string
				for _, isl := range islands {
					if reason := islandAtRisk(ctx, c, isl); reason != "" {
						atRisk = append(atRisk, fmt.Sprintf("  %s — %s", isl.Name, reason))
					}
				}
				if len(atRisk) > 0 {
					return fmt.Errorf("refusing to uninstall — these islands have at-risk or unverifiable work:\n%s\n\ncommit/push (or `dejima wake` to verify), or re-run with --force",
						strings.Join(atRisk, "\n"))
				}
			}

			selfBin, _ := os.Executable()
			daemonBin, _ := resolveDaemonBinary()
			root, _ := paths.Root()

			purge := mode == uninstallModePurgeAll

			if daemonDown != "" {
				fmt.Printf("Dejima's background service isn't answering — %s.\n", plainDaemonError(daemonDown))
				fmt.Println()
				fmt.Println("That doesn't stop this. Your islands (the separate workspaces your agents")
				fmt.Println("work in) live on whichever Mac runs Dejima, and only that Mac's service can")
				fmt.Println("change them. So this removes Dejima from THIS Mac and leaves every island")
				fmt.Println("exactly as it is — nothing of yours is deleted.")
				fmt.Println()
			}
			// "permanently" is only true when something is actually being
			// destroyed. Printing it above a list whose first line is "leave every
			// island untouched" reads as a warning about the thing it is promising
			// not to do — which is how an operator ends up scared of the safe path.
			if purge {
				fmt.Println("This will permanently:")
			} else {
				fmt.Println("This will:")
			}
			switch {
			case daemonDown != "":
				fmt.Println("  • leave your islands and everything in them alone")
			case purge:
				fmt.Printf("  • purge %s (deleting their volumes)\n", countNoun(len(islands), "island"))
			default:
				fmt.Printf("  • stop %s (KEEPING their volumes + config — a reinstall re-adopts them)\n", countNoun(len(islands), "island"))
			}
			fmt.Println("  • uninstall the dejimad service")
			if daemonBin != "" {
				fmt.Printf("  • remove %s\n", daemonBin)
			}
			if selfBin != "" {
				fmt.Printf("  • remove %s\n", selfBin)
			}
			if purge && root != "" {
				fmt.Printf("  • delete %s\n", root)
			} else if root != "" {
				fmt.Printf("  • keep %s (config, GitHub identities, ledger)\n", root)
			}
			fmt.Println()

			if !yes {
				fmt.Print("Type 'uninstall' to confirm: ")
				var in string
				_, _ = fmt.Scanln(&in)
				if strings.TrimSpace(in) != "uninstall" {
					return fmt.Errorf("aborted")
				}
			}

			// 1. Quiesce every island. The guard pre-flight already ran.
			//    --purge-all destroys volumes + config; --keep-islands only stops
			//    the live container (hibernate), leaving the volume + config on
			//    disk so a reinstall re-adopts the island by name.
			//    With the daemon down `islands` is empty, so this loop is a no-op —
			//    the leftovers get named in the closing notice instead.
			for _, isl := range islands {
				if purge {
					// Print the action BEFORE the (slow, container-stopping) call so
					// each island shows progress instead of a silent stall.
					fmt.Printf("  purging %s… ", isl.Name)
					if err := c.DeleteIsland(ctx, isl.Name, true); err != nil {
						fmt.Println("failed")
						fmt.Fprintf(os.Stderr, "  warning: purge %s: %v\n", isl.Name, err)
					} else {
						fmt.Println("done")
					}
					continue
				}
				if isl.Container != "running" {
					// Already stopped/hibernated; nothing live to quiesce.
					fmt.Printf("  kept %s (volumes + config retained)\n", isl.Name)
					continue
				}
				fmt.Printf("  stopping %s… ", isl.Name)
				if _, err := c.HibernateIsland(ctx, isl.Name); err != nil {
					fmt.Println("failed")
					fmt.Fprintf(os.Stderr, "  warning: stop %s: %v\n", isl.Name, err)
				} else {
					fmt.Println("done (kept; volumes + config retained)")
				}
			}

			// 2. Uninstall the service (stops the daemon).
			// Anything the uninstall could not finish, in the operator's words and
			// collected for the END. Mid-stream notes scroll past and are then
			// contradicted by a closing "uninstalled" line; the one thing still
			// needing a human belongs after that line, not before it.
			var leftovers []string

			if mgr, mErr := serviceMgr(systemSvc); mErr == nil {
				fmt.Println("  removing the background service…")
				switch err := mgr.Uninstall(); {
				case err == nil:
					fmt.Println("  removed the background service")
				case errors.Is(err, os.ErrNotExist):
					// No plist to delete is not a failure — there was no service
					// registered here. Reporting "failed" for nothing-to-do sends
					// people hunting for a problem that does not exist.
					fmt.Println("  no background service was registered on this Mac — nothing to remove")
				default:
					fmt.Println("  couldn't remove the background service")
					fmt.Fprintf(os.Stderr, "  warning: service uninstall: %v\n", err)
				}
			}

			// 3. Remove the binaries. A running binary can be unlinked on unix
			// (the inode lives until the process exits).
			for _, bin := range []string{daemonBin, selfBin} {
				if bin == "" {
					continue
				}
				switch err := os.Remove(bin); {
				case err == nil:
					fmt.Printf("  removed %s\n", bin)
				case errors.Is(err, os.ErrNotExist):
					// already gone
				case errors.Is(err, os.ErrPermission):
					leftovers = append(leftovers,
						fmt.Sprintf("%s needs an administrator to delete it:\n    sudo rm %s", bin, bin))
				default:
					fmt.Fprintf(os.Stderr, "  warning: remove %s: %v\n", bin, err)
				}
			}

			// 3b. Drop the provisioning wizard's progress, in BOTH modes. It lives
			// in ~/.dejima, but it is not island data: it is a record of which
			// host-setup phases ran on a host that is being torn down. Keeping it
			// makes "uninstall, then reinstall" — the operator's own remedy for a
			// broken setup — replay as "already done (skipping)" for every phase,
			// so the reinstall fixes nothing and says nothing about why.
			resetProvState()

			// 4. Delete ~/.dejima — only under --purge-all. --keep-islands keeps
			//    it (config + volume bookkeeping) so a reinstall re-adopts.
			if purge && root != "" {
				if err := os.RemoveAll(root); err != nil {
					fmt.Fprintf(os.Stderr, "  note: couldn't fully delete %s: %v (root-owned files? `sudo rm -rf %s`)\n", root, err, root)
				} else {
					fmt.Printf("  deleted %s\n", root)
				}
			}

			switch {
			case daemonDown != "":
				fmt.Print(daemonDownNotice(purge))
			case purge:
				fmt.Println("\nDone — Dejima is removed from this Mac, along with all islands and their data.")
			default:
				fmt.Println("\nDone — Dejima is removed from this Mac. Your islands and their contents")
				fmt.Println("are still there; installing Dejima again picks them back up.")
			}
			if len(leftovers) > 0 {
				fmt.Println("\nOne thing left for you to do:")
				for _, l := range leftovers {
					fmt.Printf("  • %s\n", l)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "bypass the unpushed-work guard (act even with unpushed/uncommitted work)")
	cmd.Flags().BoolVar(&systemSvc, "system", false, "uninstall the system-wide LaunchDaemon (macOS)")
	cmd.Flags().BoolVar(&keepIslands, "keep-islands", false, "remove the service + binaries + live containers, but KEEP island volumes + ~/.dejima config (a reinstall re-adopts)")
	cmd.Flags().BoolVar(&purgeAll, "purge-all", false, "delete everything: purge every island (volumes included) and ~/.dejima (irreversible)")
	cmd.Flags().BoolVar(&clientOnly, "client", false, "client-only: remove the CLI + saved server connection from THIS machine; no daemon, islands untouched")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "deprecated alias for --keep-islands")
	_ = cmd.Flags().MarkDeprecated("keep-data", "use --keep-islands")
	return cmd
}
