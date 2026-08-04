package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/localmodel"
)

// newLocalCmd is the CLI surface for managed local models: a host inference
// backend (Ollama by default) that every island drives as a thin OpenAI-
// compatible client via the auto-registered `local` provider. The model loads
// once on the host, not per island. Mirrors the TUI's "Local models" section.
func newLocalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Manage local open-weights models (a host inference backend islands share).",
		Long: "Dejima runs one inference backend (Ollama by default) on the daemon host and " +
			"registers it as the `local` LLM provider, so every island drives it as a thin " +
			"OpenAI-compatible client — the model loads once, not per island.\n\n" +
			"Bare `dejima local` shows status. See docs/local-models.md.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLocalStatus(cmd) },
	}
	cmd.AddCommand(
		newLocalStatusCmd(), newLocalInstallCmd(), newLocalModelsCmd(),
		newLocalPullCmd(), newLocalRmCmd(), newLocalOffCmd(),
	)
	return cmd
}

func newLocalStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show the local backend + pulled models.",
		Aliases: []string{"st"},
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return runLocalStatus(cmd) },
	}
}

func runLocalStatus(cmd *cobra.Command) error {
	c, err := client()
	if err != nil {
		return err
	}
	st, err := c.LocalStatus(cmd.Context())
	if err != nil {
		return err
	}
	state := "not installed"
	switch {
	case st.Running:
		state = "running"
	case st.Installed:
		state = "installed (not running)"
	}
	fmt.Printf("backend:  %s\n", st.Backend)
	fmt.Printf("status:   %s\n", state)
	fmt.Printf("endpoint: %s  (provider %q)\n", st.Endpoint, st.Provider)
	if st.HostRAMGiB > 0 {
		fmt.Printf("host RAM: %d GiB\n", st.HostRAMGiB)
	}
	if !st.Installed {
		fmt.Println("\ninstall it with `dejima local install`")
		printRecommend(st.Recommend)
		return nil
	}
	if len(st.Models) == 0 {
		fmt.Println("\nno models pulled yet — `dejima local pull <model>`")
	} else {
		fmt.Println("\npulled models:")
		printModelTable(st.Models)
	}
	printRecommend(st.Recommend)
	return nil
}

func newLocalInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the inference backend on the daemon host + register the provider.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			fmt.Println("installing the local inference backend on the daemon host…")
			if err := c.LocalInstall(cmd.Context(), os.Stdout); err != nil {
				return err
			}
			fmt.Println("\ndone — the `local` provider is registered. Pull a model: `dejima local pull <model>`")
			return nil
		},
	}
}

func newLocalModelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "models",
		Short:   "List pulled models + host-aware recommendations.",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			res, err := c.ListLocalModels(cmd.Context())
			if err != nil {
				return err
			}
			if len(res.Pulled) == 0 {
				fmt.Println("no models pulled — `dejima local pull <model>`")
			} else {
				fmt.Println("pulled:")
				printModelTable(res.Pulled)
			}
			fmt.Println("\ncurated models that fit this host:")
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "  ALIAS\tPARAMS\tMIN RAM\tNOTE")
			for _, m := range res.Recommended.Fits {
				mark := "  "
				if res.Recommended.Top != nil && res.Recommended.Top.Alias == m.Alias {
					mark = "* "
				}
				fmt.Fprintf(tw, "%s%s\t%s\t%d GiB\t%s\n", mark, m.Alias, m.Params, m.MinRAMGiB, m.Note)
			}
			tw.Flush()
			fmt.Println("\n(* = recommended default; pull with `dejima local pull <alias>`)")
			return nil
		},
	}
}

func newLocalPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <model>",
		Short: "Download a model — a curated alias (e.g. qwen-coder) or a raw backend ref.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			fmt.Printf("pulling %s…\n", args[0])
			if err := c.PullLocalModel(cmd.Context(), args[0], os.Stdout); err != nil {
				return err
			}
			fmt.Println("\ndone. Point an agent at it: set its provider to `local` (the `v` model editor).")
			return nil
		},
	}
}

func newLocalRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <model>",
		Short:   "Remove a pulled model.",
		Aliases: []string{"remove", "delete"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RemoveLocalModel(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", args[0])
			return nil
		},
	}
}

func newLocalOffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "off",
		Short: "Deregister the `local` provider (the backend + pulled models stay).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.LocalOff(cmd.Context()); err != nil {
				return err
			}
			fmt.Println("local provider disabled — islands are no longer offered it.")
			return nil
		},
	}
}

func printModelTable(models []localmodel.InstalledModel) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  REF\tSIZE\tALIAS")
	for _, m := range models {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.Ref, dashIfEmpty(m.Size), dashIfEmpty(m.Alias))
	}
	tw.Flush()
}

func printRecommend(rec localmodel.Recommendation) {
	if rec.Top == nil {
		if rec.HostRAMGiB > 0 {
			fmt.Printf("\n(no curated model fits %d GiB — pull a smaller one by ref)\n", rec.HostRAMGiB)
		}
		return
	}
	fmt.Printf("\nrecommended for this host (%d GiB): %s (%s) — `dejima local pull %s`\n",
		rec.HostRAMGiB, rec.Top.Alias, rec.Top.Params, rec.Top.Alias)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
