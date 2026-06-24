package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// Exit-ramp (no-lock-in escape hatch).
//
// `dejima eject <island> <dest-dir>` copies an island's volumes OUT to plain
// host directories, so the work keeps running with no Dejima in the loop. This
// is the stronger sibling of `uninstall --keep-islands`: that keeps your data
// adoptable *within* Dejima; eject gets it onto the host filesystem entirely.
//
// What it extracts:
//   - <dest-dir>/workspace  — the island's workspace volume: your code AND its
//     .git directory, i.e. a usable git working tree you can `cd` into and run
//     (build/test/commit/push) with no Dejima.
//   - <dest-dir>/home       — (with --include-home) the island's home volume:
//     tool credentials and agent state. Often host/device-bound, so portability
//     here is best-effort; see docs/exit-ramp.md.
//
// Guarantees:
//   - The source volume is never mutated: copies run in a throwaway container
//     that mounts the volume read-only (:ro) and only writes to the host bind.
//   - A non-empty destination is refused unless --force, so an eject can't
//     silently clobber existing host files.

// volumeNames returns the deterministic Docker volume names for an island,
// matching project.WorkspaceVolume/HomeVolume ("dejima-<island>-workspace" /
// "-home"). Kept here (rather than importing project) because the CLI addresses
// volumes by their stable name and never loads the daemon-side project config.
func volumeNames(island string) (workspace, home string) {
	return "dejima-" + island + "-workspace", "dejima-" + island + "-home"
}

// dirIsPopulated reports whether path exists and contains at least one entry.
// A missing or empty directory is a safe eject target; a populated one is the
// clobber case the --force flag guards.
func dirIsPopulated(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// ejectIdleWarning returns a human reason to warn the operator that an island
// isn't safely idle for a consistent copy, or "" when it's fine. Mirrors the
// spirit of clone's "best run when idle" caveat: a live container can be
// mid-write, and unpushed/uncommitted work means the extracted tree is a
// point-in-time snapshot, not a pushed source of truth. Non-fatal — eject is a
// rescue hatch and must still work on a busy island.
func ejectIdleWarning(isl *api.IslandInfo) string {
	if isl == nil {
		return ""
	}
	var notes []string
	if isl.Container == "running" {
		notes = append(notes, "the container is running (the copy is a point-in-time snapshot)")
	}
	if g := isl.Git; g != nil {
		if !g.Clean && g.DirtyFiles > 0 {
			notes = append(notes, countNoun(g.DirtyFiles, "uncommitted change")+" will be copied as-is")
		}
		if g.Ahead > 0 {
			notes = append(notes, countNoun(g.Ahead, "unpushed commit")+" lives only in the extracted .git")
		}
	}
	return strings.Join(notes, "; ")
}

// copyVolumeToHost copies the contents of a Docker volume into a host directory
// via a throwaway container: it mounts the volume read-only and the host dir
// read-write, then `cp -a` preserves perms/timestamps/symlinks. The source
// volume is never modified (it's :ro). image must provide a POSIX sh + cp; the
// island image does, which is why eject reuses the island's own image.
func copyVolumeToHost(ctx context.Context, volume, hostDir, image string) error {
	abs, err := filepath.Abs(hostDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	// `docker run -v <volume>:/from:ro -v <hostdir>:/to <image> cp -a /from/. /to/`
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", volume+":/from:ro",
		"-v", abs+":/to",
		image, "sh", "-c", "cp -a /from/. /to/")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker copy %s → %s: %w: %s", volume, abs, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// volumeExists reports whether a Docker volume is present on the host engine.
func volumeExists(ctx context.Context, volume string) bool {
	return exec.CommandContext(ctx, "docker", "volume", "inspect", volume).Run() == nil
}

func newEjectCmd() *cobra.Command {
	var includeHome, force, yes bool
	cmd := &cobra.Command{
		Use:   "eject <island> <dest-dir>",
		Short: "Extract an island's code (and optionally creds) to host dirs — run it without Dejima.",
		Long: "The no-lock-in escape hatch. Copies an island's volumes OUT to plain host\n" +
			"directories so the work keeps running with no Dejima in the loop:\n\n" +
			"  <dest-dir>/workspace   the island's code AND its .git — a usable git working\n" +
			"                         tree you can `cd` into and build/test/commit/push.\n" +
			"  <dest-dir>/home        (with --include-home) tool credentials + agent state.\n" +
			"                         Often host/device-bound, so this is best-effort.\n\n" +
			"The source volume is never modified (it's mounted read-only). A non-empty\n" +
			"destination is refused unless --force. Best run when the island is idle, so\n" +
			"the copy is consistent — eject warns (but proceeds) otherwise.\n\n" +
			"Stronger than `uninstall --keep-islands`: that keeps your data adoptable inside\n" +
			"Dejima; eject gets it onto the host filesystem entirely. See docs/exit-ramp.md.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, dest := args[0], args[1]
			ctx := cmd.Context()

			// Pre-flight: a non-empty dest is the clobber case. Refuse unless
			// --force so an eject never silently overwrites host files.
			workspaceDir := filepath.Join(dest, "workspace")
			homeDir := filepath.Join(dest, "home")
			for _, d := range []string{workspaceDir, homeDir} {
				if d == homeDir && !includeHome {
					continue
				}
				populated, err := dirIsPopulated(d)
				if err != nil {
					return fmt.Errorf("check destination %s: %w", d, err)
				}
				if populated && !force {
					return fmt.Errorf("destination %s is not empty — refusing to clobber (re-run with --force to overwrite)", d)
				}
			}

			// Resolve the island via the daemon: validates it exists, surfaces
			// the image to copy with, and powers the idle warning. The actual
			// copy is a host-local docker operation (eject writes to the host
			// FS, where the CLI runs), so it doesn't go through the API.
			c, err := client()
			if err != nil {
				return err
			}
			isl, err := c.GetIsland(ctx, island)
			if err != nil {
				return fmt.Errorf("island %q: %w (is the daemon running?)", island, err)
			}

			workspaceVol, homeVol := volumeNames(island)
			if !volumeExists(ctx, workspaceVol) {
				return fmt.Errorf("workspace volume %q not found on this host — eject must run on the island's Docker host", workspaceVol)
			}

			image := isl.Image
			if image == "" {
				image = api.DefaultImage
			}

			if warn := ejectIdleWarning(isl); warn != "" {
				fmt.Fprintf(os.Stderr, "note: %s — %s.\n", island, warn)
			}

			if !yes {
				fmt.Printf("Eject %q to %s? This copies the workspace", island, dest)
				if includeHome {
					fmt.Print(" and home")
				}
				fmt.Print(" volume(s) to host directories (the source is untouched). [y/N]: ")
				var in string
				_, _ = fmt.Scanln(&in)
				if s := strings.ToLower(strings.TrimSpace(in)); s != "y" && s != "yes" {
					return errors.New("aborted")
				}
			}

			copyCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()

			fmt.Printf("ejecting %q → %s (copying workspace volume, source read-only)…\n", island, dest)
			if err := copyVolumeToHost(copyCtx, workspaceVol, workspaceDir, image); err != nil {
				return fmt.Errorf("copy workspace: %w", err)
			}
			fmt.Printf("  wrote %s (code + git history)\n", workspaceDir)

			if includeHome {
				if !volumeExists(ctx, homeVol) {
					fmt.Fprintf(os.Stderr, "  note: home volume %q not found; skipping --include-home\n", homeVol)
				} else {
					fmt.Printf("  copying home volume (creds + agent state, best-effort portability)…\n")
					if err := copyVolumeToHost(copyCtx, homeVol, homeDir, image); err != nil {
						return fmt.Errorf("copy home: %w", err)
					}
					fmt.Printf("  wrote %s (tool credentials + agent state)\n", homeDir)
				}
			}

			fmt.Printf("\nEjected. %s is a plain git working tree — `cd` in and run it without Dejima.\n", workspaceDir)
			if !includeHome {
				fmt.Println("(tool credentials/state were not copied; re-run with --include-home to include them.)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&includeHome, "include-home", false, "also copy the home volume (tool credentials + agent state; host/device-bound, best-effort)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite a non-empty destination directory")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
