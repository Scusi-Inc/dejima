package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/reposrc"
)

// --- home -----------------------------------------------------------------

func newHomeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "home",
		Short: "Manage Home Islands — persistent islands that host an assistant brain.",
		Long: "A Home Island runs an always-on assistant orchestrator (an OpenClaw / Hermes / " +
			"Letta-style gateway) inside containment instead of native on the host. It reaches " +
			"your files only through the Port (scoped + ledgered) and spawns work islands via " +
			"the API. Containing the brain matters because it reads untrusted inbound channels " +
			"(chat, email) — the prime prompt-injection surface. Run native only when the brain " +
			"needs host-OS APIs; see `dejima home create --explain-native`.",
	}
	cmd.AddCommand(newHomeCreateCmd())
	return cmd
}

func newHomeCreateCmd() *cobra.Command {
	var (
		repo, name, image, cmdStr, agent string
		memory, cpus, disk               string
		localCopy, explainNative, noRepo bool
	)
	cmd := &cobra.Command{
		Use:   "create (--agent openclaw | --cmd \"<brain launch>\") (--repo <url> | --no-repo --name <name>)",
		Short: "Create a Home Island running an assistant brain (headless).",
		Long: "Provisions a persistent, headless island that runs an assistant brain. Pick the " +
			"brain with --agent (e.g. \"openclaw\", which self-installs and runs its gateway), or " +
			"give a raw launch with --agent headless --cmd \"…\". Grant it host files with " +
			"`dejima port grant`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if explainNative {
				printNativeGuidance()
				return nil
			}
			// Resolve the brain: a named agent (baked launch, e.g. openclaw) or the
			// reserved headless type with an explicit --cmd. --cmd alone (no --agent)
			// stays shorthand for "headless with this cmd".
			agent = strings.TrimSpace(agent)
			cmdStr = strings.TrimSpace(cmdStr)
			if agent == "" {
				if cmdStr == "" {
					return fmt.Errorf("specify the brain: --agent openclaw, or --agent headless --cmd \"<launch>\"")
				}
				agent = api.AgentHeadless
			}
			if agent == api.AgentHeadless && cmdStr == "" {
				return fmt.Errorf("--agent headless needs --cmd (the brain's launch command)")
			}
			if agent != api.AgentHeadless && cmdStr != "" {
				return fmt.Errorf("--cmd can't be combined with --agent %s (it has its own launch); use --agent headless for a raw command", agent)
			}
			// A brain that keeps no config in git is the ordinary case here, not an
			// edge one — openclaw self-installs and writes its own state. But an
			// EMPTY --repo still has to fail: a URL the shell ate looks identical to
			// a deliberate choice, and the difference only shows up later as a brain
			// with none of its config. Hence the explicit flag.
			switch {
			case noRepo && repo != "":
				return fmt.Errorf("--no-repo can't be combined with --repo — pick one")
			case noRepo && name == "":
				return fmt.Errorf("--name is required with --no-repo (there's no repo to derive it from)")
			case !noRepo && repo == "":
				return fmt.Errorf("--repo is required (the brain's config/workspace repo), or --no-repo to start it with an empty workspace")
			}
			// --no-repo has nothing to resolve: no URL, no local path, no seed.
			// Skip the resolver rather than feed it "" and rely on it declining.
			res := reposrc.Resolution{Note: "no repo — /workspace starts empty"}
			if !noRepo {
				resolved, err := reposrc.Resolve(repo, resolveHost() == "", localCopy)
				if err != nil {
					return err
				}
				res = resolved
				if name == "" {
					name = project.DeriveNameFromRepo(repo)
				}
			}
			c, err := client()
			if err != nil {
				return err
			}
			fmt.Printf("• %s\n", res.Note)
			if image == "" || image == api.DefaultImage {
				if err := ensureIslandImage(cmd.Context(), c); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			info, err := c.CreateIsland(ctx, api.CreateIslandRequest{
				Name:      name,
				Repo:      res.Repo,
				SeedPath:  res.SeedPath,
				Agent:     agent,
				Image:     image,
				Cmd:       cmdStr,
				NoRepo:    noRepo,
				Role:      project.RoleHome,
				Resources: api.Resources{Memory: memory, CPUs: cpus, Disk: disk},
			})
			if err != nil {
				return err
			}
			fmt.Printf("created home island %q (container: %s)\n", info.Name, info.Container)
			fmt.Printf("logs:           dejima logs %s --follow\n", info.Name)
			fmt.Printf("grant it files: dejima port grant %s <host-path>:ro\n", info.Name)
			fmt.Println("note: the brain drives the Port over the in-island socket on Linux daemon hosts.")
			fmt.Println("      on a macOS daemon host that socket is unavailable — enable the daemon TCP")
			fmt.Println("      listener for brain-driven Port/spawn (see `dejima service`).")
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", `the brain to run: a headless agent like "openclaw" (self-installs), or "headless" with --cmd`)
	cmd.Flags().StringVar(&cmdStr, "cmd", "", `raw launch command for --agent headless (e.g. "openclaw gateway")`)
	cmd.Flags().StringVar(&repo, "repo", "", "git repo URL or local path for the brain's config/workspace (required, unless --no-repo)")
	cmd.Flags().BoolVar(&noRepo, "no-repo", false, "start the brain with an empty /workspace and no origin — for brains that self-install and keep no config in git (requires --name)")
	cmd.Flags().BoolVar(&localCopy, "local-copy", false, "for a local path: seed from the working copy on disk instead of cloning from origin")
	cmd.Flags().StringVar(&name, "name", "", "island name (default: derived from repo)")
	cmd.Flags().StringVar(&image, "image", "", "island image (default: dejima/island:latest)")
	cmd.Flags().StringVar(&memory, "memory", "", "memory limit (e.g. 4G); default: unlimited")
	cmd.Flags().StringVar(&cpus, "cpus", "", "CPU limit (e.g. 2.0); default: unlimited")
	cmd.Flags().StringVar(&disk, "disk", "", "disk size (e.g. 20G); default: unlimited")
	cmd.Flags().BoolVar(&explainNative, "explain-native", false, "explain when to run the brain native on the host instead, and how")
	return cmd
}

func printNativeGuidance() {
	fmt.Println(`Run the brain NATIVE on the host (not in a Home Island) only when it needs
host-OS-native capabilities a container can't reach through a file broker:
  · macOS Shortcuts / Automations
  · Apple Notes / Reminders (APIs, not files)
  · iMessage / native-app control (AppleScript)

For everything that is files + code execution, prefer a Home Island: the brain
stays contained, reaches your files only through the scoped + ledgered Port, and
spawns work islands via the API. Running native trades that containment for
host-OS reach — Dejima then contains the brain's file/code actions (Port + work
islands) but not the orchestrator itself.`)
}
