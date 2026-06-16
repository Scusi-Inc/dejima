package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/homeconfig"
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
	cmd.AddCommand(newHomeConfigureCmd())
	cmd.AddCommand(newHomeDoctorCmd())
	return cmd
}

func newHomeCreateCmd() *cobra.Command {
	var (
		repo, name, image, cmdStr string
		memory, cpus, disk        string
		localCopy, explainNative  bool
	)
	cmd := &cobra.Command{
		Use:   "create --cmd \"<brain launch>\" --repo <url>",
		Short: "Create a Home Island running an assistant brain (headless).",
		Long: "Provisions a persistent, headless island whose command is the assistant brain's " +
			"launch (e.g. \"openclaw gateway\"). Grant it host files with `dejima port grant`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if explainNative {
				printNativeGuidance()
				return nil
			}
			if strings.TrimSpace(cmdStr) == "" {
				return fmt.Errorf(`--cmd is required: the brain's launch command (e.g. "openclaw gateway")`)
			}
			if repo == "" {
				return fmt.Errorf("--repo is required (the brain's config/workspace repo); repo-less home islands are a follow-up")
			}
			res, err := reposrc.Resolve(repo, os.Getenv("DEJIMA_HOST") == "", localCopy)
			if err != nil {
				return err
			}
			if name == "" {
				name = project.DeriveNameFromRepo(repo)
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
				Agent:     api.AgentHeadless,
				Image:     image,
				Cmd:       cmdStr,
				Role:      project.RoleHome,
				Resources: api.Resources{Memory: memory, CPUs: cpus, Disk: disk},
			})
			if err != nil {
				return err
			}
			printHomeCreated(ctx, c, &info.IslandInfo)
			return nil
		},
	}
	cmd.Flags().StringVar(&cmdStr, "cmd", "", `the brain's launch command (e.g. "openclaw gateway") (required)`)
	cmd.Flags().StringVar(&repo, "repo", "", "git repo URL or local path for the brain's config/workspace (required)")
	cmd.Flags().BoolVar(&localCopy, "local-copy", false, "for a local path: seed from the working copy on disk instead of cloning from origin")
	cmd.Flags().StringVar(&name, "name", "", "island name (default: derived from repo)")
	cmd.Flags().StringVar(&image, "image", "", "island image (default: dejima/island:latest)")
	cmd.Flags().StringVar(&memory, "memory", "", "memory limit (e.g. 4G); default: unlimited")
	cmd.Flags().StringVar(&cpus, "cpus", "", "CPU limit (e.g. 2.0); default: unlimited")
	cmd.Flags().StringVar(&disk, "disk", "", "disk size (e.g. 20G); default: unlimited")
	cmd.Flags().BoolVar(&explainNative, "explain-native", false, "explain when to run the brain native on the host instead, and how")
	return cmd
}

// printHomeCreated prints the ordered next-steps after a Home Island is created.
// The autonomy line is gated on the daemon actually reporting the in-island token
// path live, so the operator learns up front whether brain-driven Port/spawn will
// work (and how to fix it if not) instead of discovering it later.
func printHomeCreated(ctx context.Context, c *api.Client, info *api.IslandInfo) {
	agentID := ""
	if len(info.Agents) > 0 {
		agentID = info.Agents[0].ID
	}
	logsTarget := info.Name
	if agentID != "" {
		logsTarget = info.Name + " --agent " + agentID
	}
	fmt.Printf("created home island %q (container: %s)\n", info.Name, info.Container)
	fmt.Println("next:")
	fmt.Printf("  1. configure:   dejima home configure %s --scaffold\n", info.Name)
	fmt.Printf("  2. grant files: dejima port grant %s <host-path>:ro\n", info.Name)
	fmt.Printf("  3. watch:       dejima logs %s --follow\n", logsTarget)
	fmt.Printf("  4. verify:      dejima home doctor %s\n", info.Name)

	// Autonomy status decides whether the brain can drive the Port/spawn on its
	// own. On Linux daemon hosts the in-island unix socket covers this; on macOS
	// it needs the daemon's token listener (`dejimad --token-tcp`).
	if o, err := c.Overview(ctx); err == nil {
		if o.AutonomyEnabled {
			fmt.Println("autonomy: live — the brain can drive the Port and spawn islands.")
		} else {
			fmt.Println("autonomy: OFF — the brain can't drive the Port/spawn on its own yet.")
			fmt.Println("          start the daemon with `dejimad --token-tcp 127.0.0.1:7274`")
			fmt.Println("          (or `dejima service install --token-tcp 127.0.0.1:7274`) to enable it.")
		}
	}
}

// newHomeConfigureCmd scaffolds a brain's config into the island workspace so it
// stops idling in --allow-unconfigured mode and starts doing work. It writes into
// the island's /workspace (never the host) and best-effort reloads the agent so
// the new config takes effect. Secrets are never scaffolded — see the printed
// guidance and the scaffolded SECRETS.md.
func newHomeConfigureCmd() *cobra.Command {
	var (
		framework string
		scaffold  bool
		force     bool
		noReload  bool
	)
	cmd := &cobra.Command{
		Use:   "configure <island>",
		Short: "Scaffold an assistant brain's config into a Home Island's workspace.",
		Long: "Writes a starting-point config (e.g. openclaw.config.toml) into the island's " +
			"/workspace so the brain stops idling unconfigured. The scaffold is a template you " +
			"edit and then commit to the brain's config repo. Secrets are never written to disk; " +
			"the command prints the two safe ways to supply channel credentials.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !scaffold {
				return fmt.Errorf("nothing to do: pass --scaffold to write the %s config scaffold", framework)
			}
			files, err := homeconfig.Template(framework)
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			wrote := 0
			for _, f := range files {
				wsPath := "workspace/" + f.Path // → /workspace/<path> in the island
				if !force {
					if rc, err := c.ReadFile(ctx, name, "/workspace/"+f.Path); err == nil {
						rc.Close()
						fmt.Printf("• %s already exists — left as-is (use --force to overwrite)\n", f.Path)
						continue
					}
				}
				if err := c.WriteFile(ctx, name, wsPath, bytes.NewReader(f.Body)); err != nil {
					return fmt.Errorf("write %s: %w", f.Path, err)
				}
				fmt.Printf("• wrote /workspace/%s\n", f.Path)
				wrote++
			}

			if wrote == 0 {
				fmt.Println("no files written; config already present.")
				return nil
			}

			// Reload the brain so it re-reads the new config. The headless launch
			// runs under a restart loop, so terminating the process makes the
			// supervisor relaunch it cleanly (no state-volume wipe like `reset`).
			if !noReload {
				reloadBrain(ctx, c, name, framework)
			}

			fmt.Println()
			fmt.Println("next:")
			fmt.Printf("  • edit /workspace/%s, then commit it to the brain's config repo so it survives a rebuild\n", homeconfig.ConfigPath(framework))
			fmt.Println("  • channel credentials (never scaffolded — see /workspace/SECRETS.md):")
			fmt.Printf("      in-island:  dejima exec %s -- %s secrets set <channel> <token>\n", name, framework)
			fmt.Printf("      brokered:   dejima port grant %s ~/brain-secrets:ro && dejima port intake %s brain-secrets:creds.env\n", name, name)
			fmt.Printf("  • verify:     dejima home doctor %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&framework, "framework", "openclaw", "assistant framework to scaffold config for")
	cmd.Flags().BoolVar(&scaffold, "scaffold", false, "write the config scaffold into the island workspace")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing scaffold files")
	cmd.Flags().BoolVar(&noReload, "no-reload", false, "don't reload the brain after writing config")
	return cmd
}

// reloadBrain best-effort restarts the brain process so it re-reads workspace
// config. It relies on the headless restart loop relaunching the process; a
// non-running island or a process that can't be matched is reported, not fatal.
func reloadBrain(ctx context.Context, c *api.Client, name, framework string) {
	info, err := c.GetIsland(ctx, name)
	if err != nil || info.Container != "running" {
		fmt.Println("• island not running — config will be picked up on next start")
		return
	}
	// pkill the framework process; the supervisor's restart loop relaunches it.
	res, err := c.ExecInIsland(ctx, name, []string{"pkill", "-TERM", "-f", framework})
	if err != nil {
		fmt.Printf("• couldn't reload the brain (%v) — restart it to apply config\n", err)
		return
	}
	// pkill exits 1 when nothing matched; treat that as "wasn't running yet".
	if res.ExitCode == 0 {
		fmt.Println("• reloaded the brain to apply the new config")
	} else {
		fmt.Println("• config written; brain will read it when it next starts")
	}
}

// newHomeDoctorCmd answers, in one command, "is this Home Island ready and does
// it have the access it needs?" — the question that otherwise takes a handful of
// separate `ls`/`logs`/`port list`/`exec curl` probes. It reuses the same report
// table and FAIL→nonzero-exit contract as `dejima doctor`.
func newHomeDoctorCmd() *cobra.Command {
	var framework string
	cmd := &cobra.Command{
		Use:   "doctor <island>",
		Short: "Check a Home Island is ready: agent alive, autonomy, scopes, config, creds.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			report := runHomeDoctor(ctx, c, name, framework)
			report.write(cmd.OutOrStdout())
			if report.hasFailures() {
				return fmt.Errorf("dejima home doctor: %d check(s) failed", report.failures())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&framework, "framework", "openclaw", "assistant framework (selects the config file to check for)")
	return cmd
}

// runHomeDoctor builds the readiness report for a Home Island.
func runHomeDoctor(ctx context.Context, c *api.Client, name, framework string) *doctorReport {
	r := &doctorReport{}

	info, err := c.GetIsland(ctx, name)
	if err != nil {
		r.add(name, "island", "FAIL", err.Error(), "is the name right? `dejima ls`")
		return r
	}
	if info.Role != project.RoleHome {
		r.add(name, "role", "WARN", "not a Home Island (role="+orNone(info.Role)+")",
			"home doctor is meant for islands made with `dejima home create`")
	}

	// 1. Agent process alive (a live container can still hold a dead brain).
	switch {
	case info.Container != "running":
		r.add(name, "container", "FAIL", "container "+info.Container,
			fmt.Sprintf("dejima wake %s", name))
	default:
		r.add(name, "container", "OK", "running", "")
		brainDead := false
		for _, a := range info.Agents {
			if a.State == "exited" {
				brainDead = true
				r.add(name, "agent "+a.ID, "FAIL", "process died (only a shell remains)",
					fmt.Sprintf("dejima logs %s --agent %s to see why; restart the island", name, a.ID))
			}
		}
		if !brainDead {
			r.add(name, "agent", "OK", "brain process running", "")
		}
		if info.Health != nil && info.Health.OOMKilled {
			r.add(name, "health", "WARN", "last run OOM-killed", "raise --memory or reduce the brain's footprint")
		}
	}

	// 2. Autonomy: can the brain drive the Port/spawn on its own?
	checkHomeAutonomy(ctx, c, name, info, r)

	// 3. Port scopes: deny-all by default, so zero scopes means no host files.
	if scopes, err := c.ListPortScopes(ctx, name); err == nil {
		if len(scopes.Scopes) == 0 {
			r.add(name, "port scopes", "WARN", "none granted — the brain can reach no host files (deny-all)",
				fmt.Sprintf("dejima port grant %s <host-path>:ro", name))
		} else {
			parts := make([]string, 0, len(scopes.Scopes))
			for _, s := range scopes.Scopes {
				parts = append(parts, s.Name+":"+s.Mode)
			}
			r.add(name, "port scopes", "OK", fmt.Sprintf("%d (%s)", len(scopes.Scopes), strings.Join(parts, ", ")), "")
		}
	}

	// 4. Config present: without it the brain idles in --allow-unconfigured.
	if info.Container == "running" {
		cfg := homeconfig.ConfigPath(framework)
		if cfg == "" {
			cfg = framework + " config"
		}
		if res, err := c.ExecInIsland(ctx, name, []string{"test", "-f", "/workspace/" + cfg}); err == nil && res.ExitCode == 0 {
			r.add(name, "config", "OK", "/workspace/"+cfg+" present", "")
		} else {
			r.add(name, "config", "FAIL", "no /workspace/"+cfg+" — the brain is idling unconfigured",
				fmt.Sprintf("dejima home configure %s --scaffold", name))
		}
	}

	// 5. Channel creds: can't introspect framework state safely, so this is a
	// reminder, not a hard check — secrets live in the home volume, not the repo.
	r.add(name, "channel creds", "INFO",
		"supply via `dejima exec … secrets set` or a brokered creds file — see /workspace/SECRETS.md", "")

	return r
}

// checkHomeAutonomy reports whether brain-driven Port/spawn is available and, on
// a running island, probes that the in-island CLI can actually reach the daemon
// (the runbook's host.docker.internal reachability test, built in).
func checkHomeAutonomy(ctx context.Context, c *api.Client, name string, info *api.IslandInfo, r *doctorReport) {
	o, err := c.Overview(ctx)
	if err != nil {
		return
	}
	if !o.AutonomyEnabled {
		r.add(name, "autonomy", "WARN", "off — the brain can't drive the Port/spawn on its own",
			"start dejimad with `--token-tcp 127.0.0.1:7274` (Linux uses the in-island socket instead)")
		return
	}
	if info.Container != "running" {
		r.add(name, "autonomy", "OK", "enabled (dial "+o.AutonomyDial+")", "")
		return
	}
	// Probe the real path the brain uses: dial the daemon's health endpoint from
	// inside the island via the injected DEJIMA_HOST.
	probe := `curl -sS -m 3 -o /dev/null -w '%{http_code}' "http://$DEJIMA_HOST/v1/healthz" 2>/dev/null || echo dial-failed`
	res, err := c.ExecInIsland(ctx, name, []string{"sh", "-lc", probe})
	switch {
	case err != nil:
		r.add(name, "autonomy", "WARN", "enabled but in-island probe failed: "+err.Error(), "")
	case strings.TrimSpace(res.Stdout) == "200":
		r.add(name, "autonomy", "OK", "live — in-island reached the daemon ("+o.AutonomyDial+")", "")
	default:
		r.add(name, "autonomy", "FAIL",
			"enabled but the in-island dial to "+o.AutonomyDial+" didn't reach the daemon",
			"check the daemon's --autonomy-dial route for this Docker runtime")
	}
}

func orNone(s string) string {
	if s == "" {
		return "work"
	}
	return s
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
