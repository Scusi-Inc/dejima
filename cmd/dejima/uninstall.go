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
			islands, err := c.ListIslands(ctx)
			if err != nil {
				return fmt.Errorf("list islands: %w (is the daemon running?)", err)
			}

			// Pre-flight the unpushed-work guard across ALL islands first, so we
			// never touch some and then abort on a guarded one (half-uninstall).
			// keep-islands preserves volumes, so unpushed work isn't lost — but
			// removing a live container still interrupts in-flight work, so we
			// guard both modes uniformly.
			if !force {
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

			fmt.Println("This will permanently:")
			if purge {
				fmt.Printf("  • purge %s (deleting their volumes)\n", countNoun(len(islands), "island"))
			} else {
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
			if mgr, mErr := serviceMgr(systemSvc); mErr == nil {
				fmt.Println("  uninstalling the dejimad service…")
				if err := mgr.Uninstall(); err != nil {
					fmt.Println("  service uninstall: failed")
					fmt.Fprintf(os.Stderr, "  warning: service uninstall: %v\n", err)
				} else {
					fmt.Println("  service uninstall: done")
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
					fmt.Fprintf(os.Stderr, "  note: couldn't remove %s (permission) — `sudo rm %s`\n", bin, bin)
				default:
					fmt.Fprintf(os.Stderr, "  warning: remove %s: %v\n", bin, err)
				}
			}

			// 4. Delete ~/.dejima — only under --purge-all. --keep-islands keeps
			//    it (config + volume bookkeeping) so a reinstall re-adopts.
			if purge && root != "" {
				if err := os.RemoveAll(root); err != nil {
					fmt.Fprintf(os.Stderr, "  note: couldn't fully delete %s: %v (root-owned files? `sudo rm -rf %s`)\n", root, err, root)
				} else {
					fmt.Printf("  deleted %s\n", root)
				}
			}

			if purge {
				fmt.Println("\nDejima uninstalled. All islands and data removed.")
			} else {
				fmt.Println("\nDejima uninstalled. Your islands' volumes + config remain; a reinstall re-adopts them.")
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
