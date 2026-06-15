package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/sshfacade"
	"github.com/aoos/dejima/internal/version"
)

// newDoctorCmd produces a single-shot health-check for the host's dejima state.
//
// Each check returns (passed, message). The command exits non-zero if any
// FAIL row is present so it composes with CI / status scripts.
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the host: daemon, Docker, image, projects, networks, webhooks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			report := runDoctor(ctx)
			report.write(cmd.OutOrStdout())
			if report.hasFailures() {
				return fmt.Errorf("dejima doctor: %d check(s) failed", report.failures())
			}
			return nil
		},
	}
}

type doctorRow struct {
	section string
	check   string
	status  string // OK | WARN | FAIL | INFO
	detail  string
	fix     string
}

type doctorReport struct {
	rows []doctorRow
}

func (r *doctorReport) add(section, check, status, detail, fix string) {
	r.rows = append(r.rows, doctorRow{section, check, status, detail, fix})
}

func (r *doctorReport) hasFailures() bool {
	for _, row := range r.rows {
		if row.status == "FAIL" {
			return true
		}
	}
	return false
}

func (r *doctorReport) failures() int {
	n := 0
	for _, row := range r.rows {
		if row.status == "FAIL" {
			n++
		}
	}
	return n
}

func (r *doctorReport) write(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	currentSection := ""
	for _, row := range r.rows {
		if row.section != currentSection {
			if currentSection != "" {
				_ = tw.Flush()
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s\n", row.section)
			currentSection = row.section
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", row.check, row.status, row.detail)
		if row.fix != "" {
			fmt.Fprintf(tw, "    fix: %s\t\t\n", row.fix)
		}
	}
	_ = tw.Flush()
}

// checkSSHFacade reports the host-side state of the SSH-façade: whether the
// daemon host key exists (and its fingerprint, for clients to pin). It's INFO,
// not FAIL — SSH is opt-in (`dejimad --ssh <addr>`), and the key materializes on
// first start.
func checkSSHFacade(r *doctorReport) {
	p, err := paths.HostKeyPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(p); err != nil {
		r.add("System", "ssh façade", "INFO",
			"no host key yet — enable with `dejimad --ssh <addr>`, then `dejima ssh authorize <island>`", "")
		return
	}
	signer, err := sshfacade.HostSigner()
	if err != nil {
		r.add("System", "ssh façade", "WARN", "host key present but unreadable: "+err.Error(),
			"remove "+p+" to regenerate (clients must re-pin)")
		return
	}
	r.add("System", "ssh façade", "OK", "host key "+sshfacade.Fingerprint(signer), "")
}

func runDoctor(ctx context.Context) *doctorReport {
	r := &doctorReport{}

	// --- System ---------------------------------------------------------
	checkDaemon(ctx, r)
	checkDocker(ctx, r)
	checkIslandImage(ctx, r)
	checkTailscale(ctx, r)
	checkClaudeCreds(ctx, r)
	checkSSHFacade(r)

	// --- Projects -------------------------------------------------------
	c, err := client()
	if err == nil {
		islands, lsErr := c.ListIslands(ctx)
		if lsErr == nil {
			if len(islands) == 0 {
				r.add("Projects", "(none)", "INFO", "no islands yet; `dejima init --repo …` to create one", "")
			}
			for _, info := range islands {
				switch {
				case info.State == "running" && info.Container != "running":
					r.add("Projects", info.Name, "FAIL",
						fmt.Sprintf("container %s but desired running", info.Container),
						fmt.Sprintf("dejima wake %s", info.Name))
				case info.Container == "missing":
					r.add("Projects", info.Name, "FAIL", "container missing",
						fmt.Sprintf("dejima wake %s (recreates) or dejima purge %s", info.Name, info.Name))
				default:
					detail := fmt.Sprintf("%s (%s)", info.Container, info.Agent)
					if info.Stats != nil {
						detail = fmt.Sprintf("%s, %s mem, %.0f%% cpu",
							detail, humanBytes(info.Stats.MemoryUsageBytes), info.Stats.CPUPercent)
					}
					r.add("Projects", info.Name, "OK", detail, "")
					// The list view doesn't probe agent liveness; fetch detail for
					// running islands so a dead agent in a live container is flagged.
					if info.Container == "running" {
						if d, err := c.GetIsland(ctx, info.Name); err == nil {
							for _, a := range d.Agents {
								if a.State == "exited" {
									r.add("Projects", info.Name+"/"+a.ID, "WARN",
										"agent process died (session alive on a shell prompt)",
										fmt.Sprintf("dejima connect %s/%s to inspect, or restart the agent", info.Name, a.ID))
								}
							}
						}
					}
				}
			}
		} else {
			r.add("Projects", "(list)", "FAIL", lsErr.Error(), "")
		}

		// --- Subscriptions ---
		if subs, err := c.ListWebhooks(ctx); err == nil {
			if len(subs) == 0 {
				r.add("Subscriptions", "webhooks", "INFO", "no webhook subscriptions", "")
			} else {
				for _, s := range subs {
					r.add("Subscriptions", s.ID[:min(8, len(s.ID))], "OK", s.URL, "")
				}
			}
		}
	}

	return r
}

func checkDaemon(ctx context.Context, r *doctorReport) {
	c, err := client()
	if err != nil {
		r.add("System", "daemon", "FAIL", err.Error(), "")
		return
	}
	if err := c.Health(ctx); err != nil {
		r.add("System", "daemon", "FAIL", err.Error(),
			"start dejimad: `dejima service install` or `dejimad --foreground`")
		return
	}
	r.add("System", "daemon", "OK", "reachable", "")

	// Version + client/daemon skew (a stale remote daemon silently misbehaves).
	if o, err := c.Overview(ctx); err == nil {
		if skew := versionSkew(o.DaemonVersion, o.APIVersion); skew != "" {
			r.add("System", "version", "WARN", skew,
				"update the older side (`make install`, then restart dejimad)")
		} else {
			r.add("System", "version", "OK",
				fmt.Sprintf("client %s · daemon %s (api v%d)", version.Version, o.DaemonVersion, o.APIVersion), "")
		}
		// Panic mode is easy to forget you left engaged — every island stays down.
		if o.Panicked {
			r.add("System", "panic", "WARN", "PANIC engaged — all islands stopped, auto-restart blocked",
				"`dejima panic --clear` to resume")
		}
	}
}

func checkDocker(ctx context.Context, r *doctorReport) {
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		r.add("System", "docker", "FAIL", "not reachable; install Docker Desktop (recommended), OrbStack, or colima",
			"https://www.docker.com/products/docker-desktop/")
		return
	}
	r.add("System", "docker", "OK", "server "+strings.TrimSpace(string(out)), "")
}

func checkIslandImage(ctx context.Context, r *doctorReport) {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "dejima/island:latest",
		"--format", "{{.Id}}").Output()
	if err != nil {
		r.add("System", "island image", "WARN", "dejima/island:latest not built locally",
			"run `make image`")
		return
	}
	id := strings.TrimSpace(string(out))
	if len(id) > 19 {
		id = id[:19]
	}
	r.add("System", "island image", "OK", id, "")
}

// checkClaudeCreds reports whether new islands will get Claude credentials.
// Without them every island starts with an interactive login prompt the user
// can't realistically complete from inside a container.
func checkClaudeCreds(ctx context.Context, r *doctorReport) {
	c, err := client()
	if err != nil {
		return // daemon check already reports the connection failure
	}
	st, err := c.ClaudeCredentialsStatus(ctx)
	if err != nil {
		return // older daemon without the endpoint; version check flags the skew
	}
	switch {
	case st.HostSource != "":
		r.add("System", "claude creds", "OK", "daemon host logged in ("+st.HostSource+")", "")
	case st.SeedPresent:
		r.add("System", "claude creds", "OK", "seeded via `dejima auth push`", "")
	default:
		r.add("System", "claude creds", "FAIL", "new islands will start without Claude credentials",
			"run `dejima auth push` from a machine where `claude` is logged in")
	}
}

func checkTailscale(ctx context.Context, r *doctorReport) {
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		r.add("System", "tailscale", "INFO", "not installed (local-only mode is fine)", "")
		return
	}
	r.add("System", "tailscale", "OK", strings.TrimSpace(string(out)), "")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
