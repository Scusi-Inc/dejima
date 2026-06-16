package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/service"
	"github.com/aoos/dejima/internal/sshfacade"
	"github.com/aoos/dejima/internal/version"
)

// newDoctorCmd produces a single-shot health-check for the host's dejima state.
//
// Each check returns (passed, message). The command exits non-zero if any
// FAIL row is present so it composes with CI / status scripts.
func newDoctorCmd() *cobra.Command {
	var fix, yes bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the host: daemon, Docker, image, projects, networks, webhooks.",
		Long: "Reports daemon / Docker / image / supervision / project health with actionable " +
			"fix hints. With --fix, auto-applies the remediations operators otherwise run by " +
			"hand (boot-persistent supervision, ~/.dejima ownership, systemd linger). Exits " +
			"non-zero if any check still fails.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			report := runDoctor(ctx)
			report.write(cmd.OutOrStdout())
			if fix {
				if err := report.applyFixes(ctx, cmd.OutOrStdout(), cmd.InOrStdin(), yes); err != nil {
					return err
				}
				// Re-run so the operator sees the post-fix state and the exit code
				// reflects what's still broken.
				report = runDoctor(ctx)
				fmt.Fprintln(cmd.OutOrStdout(), "\n— after fixes —")
				report.write(cmd.OutOrStdout())
			}
			if report.hasFailures() {
				return fmt.Errorf("dejima doctor: %d check(s) failed", report.failures())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "auto-apply remediations for fixable findings")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --fix, don't prompt before each remediation")
	return cmd
}

// doctorRemedy is an auto-applicable fix attached to a finding. argv is run via
// doctorExec (injectable for tests). desc is shown before running. privileged
// marks fixes that need sudo or change supervision — those always confirm, even
// under --yes, unless the row is non-security.
type doctorRemedy struct {
	desc string
	argv []string
}

// doctorExec runs a remediation command. Overridable in tests.
var doctorExec = func(ctx context.Context, argv []string, out io.Writer) error {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdout, c.Stderr = out, out
	c.Stdin = os.Stdin
	return c.Run()
}

type doctorRow struct {
	section string
	check   string
	status  string // OK | WARN | FAIL | INFO
	detail  string
	fix     string
	remedy  *doctorRemedy
}

type doctorReport struct {
	rows []doctorRow
}

func (r *doctorReport) add(section, check, status, detail, fix string) {
	r.rows = append(r.rows, doctorRow{section: section, check: check, status: status, detail: detail, fix: fix})
}

// addFix records a finding that `--fix` can remediate by running remedy.argv.
func (r *doctorReport) addFix(section, check, status, detail, fix string, remedy *doctorRemedy) {
	r.rows = append(r.rows, doctorRow{section: section, check: check, status: status, detail: detail, fix: fix, remedy: remedy})
}

// applyFixes runs the remedy for each non-OK row that has one, confirming first
// unless yes. It never touches OK/INFO rows. Errors are reported and collected
// but don't abort the loop — one failed fix shouldn't block the rest.
func (r *doctorReport) applyFixes(ctx context.Context, out io.Writer, in io.Reader, yes bool) error {
	var failed int
	ran := false
	for _, row := range r.rows {
		if row.remedy == nil || row.status == "OK" || row.status == "INFO" {
			continue
		}
		ran = true
		fmt.Fprintf(out, "\nfix %s / %s: %s\n  $ %s\n", row.section, row.check, row.remedy.desc, strings.Join(row.remedy.argv, " "))
		if !yes && !confirm(out, in, "  apply this fix?") {
			fmt.Fprintln(out, "  skipped")
			continue
		}
		if err := doctorExec(ctx, row.remedy.argv, out); err != nil {
			fmt.Fprintf(out, "  fix failed: %v\n", err)
			failed++
		} else {
			fmt.Fprintln(out, "  done")
		}
	}
	if !ran {
		fmt.Fprintln(out, "\nnothing to auto-fix.")
	}
	if failed > 0 {
		return fmt.Errorf("%d fix(es) failed", failed)
	}
	return nil
}

// confirm prompts y/N on out, reading a line from in. Defaults to no.
func confirm(out io.Writer, in io.Reader, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	br := bufio.NewReader(in)
	line, _ := br.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
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
	checkDejimaOwnership(r)
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
	self := selfBinary()
	switch {
	case sup.Mode == "unknown":
		return // unsupported OS; nothing useful to say
	case !sup.Managed && sup.Mode == "none":
		if reachable {
			r.addFix("System", "supervision", "WARN",
				"daemon is reachable but unsupervised (hand-run) — it won't survive a reboot",
				"install it as a service: `dejima service install` (`--system` on a headless Mac)",
				&doctorRemedy{desc: "install dejimad as a host service", argv: []string{self, "service", "install"}})
		}
		// Not reachable + unmanaged: checkDaemon already FAILed; stay quiet.
	case !sup.Managed: // e.g. system plist present but not loaded
		r.addFix("System", "supervision", "WARN", sup.Summary, sup.Concern, supervisionRemedy(sup, self))
	case sup.Concern != "":
		r.addFix("System", "supervision", "WARN", sup.Summary, sup.Concern, supervisionRemedy(sup, self))
	default:
		r.add("System", "supervision", "OK", sup.Summary, "")
	}
}

// supervisionRemedy turns a Supervision concern into a runnable fix, or nil.
func supervisionRemedy(sup service.Supervision, self string) *doctorRemedy {
	argv := sup.FixArgv(self)
	if argv == nil {
		return nil
	}
	return &doctorRemedy{desc: "make dejimad's supervision reboot-durable", argv: argv}
}

// checkDejimaOwnership flags a root-owned ~/.dejima — the classic footgun from
// running the daemon (or `service install`) under sudo without $SUDO_USER
// recovery, which leaves the control dir unwritable by the real user.
func checkDejimaOwnership(r *doctorReport) {
	root, err := paths.Root()
	if err != nil {
		return
	}
	info, err := os.Stat(root)
	if err != nil {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return // non-POSIX; nothing to check
	}
	me, err := user.Current()
	if err != nil {
		return
	}
	if fmt.Sprint(st.Uid) == me.Uid {
		r.add("System", "ownership", "OK", root+" owned by "+me.Username, "")
		return
	}
	r.addFix("System", "ownership", "WARN",
		fmt.Sprintf("%s is owned by uid %d, not you (%s) — the CLI may fail to write config", root, st.Uid, me.Username),
		"chown it back: `sudo chown -R "+me.Username+" "+root+"`",
		&doctorRemedy{desc: "return ~/.dejima to your account",
			argv: []string{"sudo", "chown", "-R", me.Username, root}})
}

// selfBinary resolves the path to this dejima binary for fixes that re-run a
// dejima subcommand (reusing its install/sudo logic). Falls back to "dejima".
func selfBinary() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "dejima"
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
