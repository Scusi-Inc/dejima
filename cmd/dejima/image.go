package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// newImageCmd manages the island image on the daemon host. The build context
// is embedded in the dejimad binary, so no source checkout is needed.
func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage the island image on the daemon host.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "build",
		Short: "(Re)build dejima/island:latest from the daemon's embedded build context.",
		Long: "The daemon carries the island image's Dockerfile and shims inside its own " +
			"binary, so the image can be rebuilt on the daemon host without a source " +
			"checkout. Existing islands keep running on the old image until upgraded " +
			"(`dejima upgrade <name>`).",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			fmt.Println("building island image on the daemon host…")
			if err := c.BuildImage(cmd.Context(), os.Stdout); err != nil {
				return err
			}
			fmt.Println("\nimage built. roll islands onto it with `dejima upgrade <name>` (or --all).")
			return nil
		},
	})
	return cmd
}

// newUpgradeCmd recreates island containers against the current island image.
func newUpgradeCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "upgrade [name]",
		Short: "Recreate island container(s) against the current island image.",
		Long: "Stops and recreates the container while preserving the workspace and agent " +
			"state, picking up a freshly built island image and any new daemon-provided " +
			"mounts (e.g. seeded credentials). Run `dejima image build` first if you want " +
			"a newer image.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 0) {
				return fmt.Errorf("name one island or pass --all (not both)")
			}
			c, err := client()
			if err != nil {
				return err
			}
			names := args
			if all {
				islands, err := c.ListIslands(cmd.Context())
				if err != nil {
					return err
				}
				names = names[:0]
				for _, i := range islands {
					names = append(names, i.Name)
				}
				if len(names) == 0 {
					fmt.Println("no islands to upgrade")
					return nil
				}
			}
			for _, name := range names {
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				info, err := c.UpgradeIsland(ctx, name)
				cancel()
				if err != nil {
					return fmt.Errorf("upgrade %s: %w", name, err)
				}
				fmt.Printf("upgraded %s (container: %s)\n", info.Name, info.Container)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "upgrade every island")
	return cmd
}

// ensureIslandImage builds the island image via the daemon when it's missing,
// streaming build output. A first `dejima init` on a fresh host Just Works
// instead of failing with "build it with make image first".
func ensureIslandImage(ctx context.Context, c *api.Client) error {
	o, err := c.Overview(ctx)
	if err != nil {
		return nil // old daemon or transient error; let the create call surface it
	}
	if o.IslandImagePresent {
		return nil
	}
	fmt.Println("• island image not present — building it now (first build takes a few minutes)")
	if err := c.BuildImage(ctx, os.Stdout); err != nil {
		return fmt.Errorf("build island image: %w", err)
	}
	fmt.Println("• island image ready")
	return nil
}
