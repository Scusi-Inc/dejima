package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/service"
	"github.com/aoos/dejima/internal/sshfacade"
	"github.com/aoos/dejima/internal/version"
	"github.com/aoos/dejima/internal/vmmem"
)

// newDoctorCmd produces a single-shot health-check for the host's dejima state.
//
// Each check returns (passed, message). The command exits non-zero if any
// FAIL row is present so it composes with CI / status scripts.
func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the host: daemon, Docker, image, connection, projects. `--fix` repairs what it safely can.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			report := runDoctor(ctx)
			report.write(cmd.OutOrStdout())
			if fix {
				report.applyFixes(cmd.OutOrStdout())
			}
			if report.hasFailures() {
				return fmt.Errorf("dejima doctor: %d check(s) failed", report.failures())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply the safe, local repairs doctor knows how to make")
	return cmd
}

type doctorRow struct {
	section string
	check   string
	status  string // OK | WARN | FAIL | INFO
	detail  string
	fix     string
	// repair, when non-nil, is a safe self-heal `--fix` runs for this row. It
	// returns a one-line outcome. Only attached to rows whose fix is local and
	// low-risk (rewrite our own state, normalize a saved profile) — never
	// anything that edits the user's shell or needs a judgment call.
	repair func() (string, error)
}

type doctorReport struct {
	rows []doctorRow
}

func (r *doctorReport) add(section, check, status, detail, fix string) {
	r.rows = append(r.rows, doctorRow{section: section, check: check, status: status, detail: detail, fix: fix})
}

// addRepair adds a row carrying a self-heal `--fix` can run.
func (r *doctorReport) addRepair(section, check, status, detail, fix string, repair func() (string, error)) {
	r.rows = append(r.rows, doctorRow{section: section, check: check, status: status, detail: detail, fix: fix, repair: repair})
}

// applyFixes runs every row's repair (for `--fix`), reporting each outcome.
func (r *doctorReport) applyFixes(w io.Writer) {
	any := false
	for _, row := range r.rows {
		if row.repair == nil {
			continue
		}
		if !any {
			fmt.Fprintln(w, "\nApplying fixes:")
			any = true
		}
		if msg, err := row.repair(); err != nil {
			fmt.Fprintf(w, "  ✗ %s — %v\n", row.check, err)
		} else {
			fmt.Fprintf(w, "  ✓ %s — %s\n", row.check, msg)
		}
	}
	if !any {
		fmt.Fprintln(w, "\nNothing to auto-fix.")
	}
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
	checkSupervision(ctx, r)
	checkDocker(ctx, r)
	checkVMMemory(ctx, r)
	checkIslandImage(ctx, r)
	checkTailscale(ctx, r)
	checkClaudeCreds(ctx, r)
	checkSSHFacade(r)

	// --- Connection & self-heal -----------------------------------------
	checkConnection(r)
	checkInstallMeta(r)
	checkStateOwnership(r)
	checkListenerExposure(r)

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

// checkSupervision answers "how is the daemon running, and will it survive a
// reboot?" — not just "is it reachable?". It flags an orphan (reachable but
// unsupervised), a per-boot/login-gated supervisor that won't come back on a
// headless host, and a system plist that's installed but not loaded.
func checkSupervision(ctx context.Context, r *doctorReport) {
	reachable := false
	if c, err := client(); err == nil {
		reachable = c.Health(ctx) == nil
	}
	sup := service.Detect()
	switch {
	case sup.Mode == "unknown":
		return // unsupported OS; nothing useful to say
	case !sup.Managed && sup.Mode == "none":
		if reachable {
			r.add("System", "supervision", "WARN",
				"daemon is reachable but unsupervised (hand-run) — it won't survive a reboot",
				"install it as a service: `dejima service install` (`--system` on a headless Mac)")
		}
		// Not reachable + unmanaged: checkDaemon already FAILed; stay quiet.
	case !sup.Managed: // e.g. system plist present but not loaded
		r.add("System", "supervision", "WARN", sup.Summary, sup.Concern)
	case sup.Concern != "":
		r.add("System", "supervision", "WARN", sup.Summary, sup.Concern)
	default:
		r.add("System", "supervision", "OK", sup.Summary, "")
	}
}

func checkDocker(ctx context.Context, r *doctorReport) {
	// Probe the *server* version: this fails when the engine is down even if the
	// docker CLI itself is fine — which lets us tell "not installed" from "not
	// started" from "can't reach the socket", and give the right fix for each
	// instead of always "go install Docker".
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err == nil {
		r.add("System", "docker", "OK", "server "+strings.TrimSpace(string(out)), "")
		return
	}

	// 1. CLI not installed at all.
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		r.add("System", "docker", "FAIL", "the docker CLI isn't installed — islands run on it",
			dockerInstallHint())
		return
	}

	// 2. Installed, but the socket refuses us (Linux: user not in the docker group).
	if strings.Contains(strings.ToLower(dockerErrText(err)), "permission denied") {
		r.add("System", "docker", "FAIL",
			"docker is installed but this user can't reach its socket (permission denied)",
			dockerPermHint())
		return
	}

	// 3. Installed but the engine/daemon isn't answering → it's not started.
	//    Name the actual runtime and how to bring it up.
	switch {
	case vmmem.ColimaAvailable():
		// colima is the engine and it's stopped — this one we can safely self-heal.
		r.addRepair("System", "docker", "FAIL",
			"docker is installed but its engine (colima) isn't running",
			"colima start",
			func() (string, error) {
				if o, e := exec.CommandContext(ctx, "colima", "start").CombinedOutput(); e != nil {
					return "", fmt.Errorf("colima start: %v: %s", e, strings.TrimSpace(string(o)))
				}
				return "colima started — docker engine is up", nil
			})
	case runtime.GOOS == "darwin":
		r.add("System", "docker", "FAIL", "docker is installed but its engine isn't running",
			"start it: `open -a Docker` (or launch OrbStack), then wait for it to come up")
	default: // Linux without colima → typically a systemd-managed dockerd
		r.add("System", "docker", "FAIL", "docker is installed but the daemon isn't running",
			"start it: `sudo systemctl start docker` (start on boot: `sudo systemctl enable --now docker`)")
	}
}

// dockerInstallHint names the per-OS way to install a container engine.
func dockerInstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "install one: `brew install --cask docker` — or colima: `brew install colima docker && colima start`"
	case "linux":
		return "install Docker engine (Debian/Ubuntu: `sudo apt install docker.io`; Fedora: `sudo dnf install docker`; Arch: `sudo pacman -S docker`), then `sudo systemctl enable --now docker`"
	default:
		return "https://www.docker.com/products/docker-desktop/"
	}
}

// dockerPermHint covers the Linux not-in-the-docker-group case (the usual
// "permission denied" on the socket).
func dockerPermHint() string {
	if runtime.GOOS == "linux" {
		return "add yourself to the docker group: `sudo usermod -aG docker $USER`, then log out and back in (or `newgrp docker`)"
	}
	return "check the docker socket's ownership/permissions"
}

// dockerErrText returns the stderr of a failed `docker version`, where the
// "Cannot connect to the Docker daemon" / "permission denied" message lives.
func dockerErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return string(ee.Stderr)
	}
	return err.Error()
}

// checkVMMemory flags a container-runtime VM that's far smaller than the host —
// the substrate cause of island OOMs (#23). On colima it offers a `--fix` that
// resizes the VM; on Docker Desktop (no CLI resize) it points at the GUI slider.
func checkVMMemory(ctx context.Context, r *doctorReport) {
	host := vmmem.HostMemoryBytes()
	if host == 0 {
		return // host RAM undeterminable — don't guess
	}
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.MemTotal}}").Output()
	if err != nil {
		return // docker unreachable — checkDocker already covers that
	}
	vm, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if vm == 0 {
		return
	}
	detail := fmt.Sprintf("VM %s of %s host RAM", humanBytes(vm), humanBytes(host))
	if !vmmem.Undersized(host, vm) {
		r.add("System", "vm memory", "OK", detail, "")
		return
	}
	recGB := vmmem.RecommendedGB(host)
	if vmmem.ColimaAvailable() {
		cpu := runtime.NumCPU() - 2
		if cpu < 2 {
			cpu = 2
		}
		r.addRepair("System", "vm memory", "WARN",
			fmt.Sprintf("%s — too small; islands share this pool and will OOM (recommend %dGB)", detail, recGB),
			fmt.Sprintf("colima stop && colima start --memory %d --cpu %d", recGB, cpu),
			func() (string, error) {
				if out, err := exec.CommandContext(ctx, "colima", "stop").CombinedOutput(); err != nil {
					return "", fmt.Errorf("colima stop: %v: %s", err, strings.TrimSpace(string(out)))
				}
				if out, err := exec.CommandContext(ctx, "colima", "start",
					"--memory", strconv.Itoa(recGB), "--cpu", strconv.Itoa(cpu)).CombinedOutput(); err != nil {
					return "", fmt.Errorf("colima start: %v: %s", err, strings.TrimSpace(string(out)))
				}
				return fmt.Sprintf("VM resized to %dGB / %d CPU — islands auto-restart", recGB, cpu), nil
			})
		return
	}
	r.add("System", "vm memory", "WARN",
		fmt.Sprintf("%s — too small; islands will OOM", detail),
		fmt.Sprintf("Docker Desktop → Settings → Resources → Memory → %dGB → Apply & Restart", recGB))
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
