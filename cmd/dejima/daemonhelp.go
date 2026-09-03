package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/service"
	"github.com/aoos/dejima/internal/wsl"
)

// daemonDiagnosis is an actionable read of *why* the local dejimad can't be
// reached and *what to do about it* — the half the bare "daemon unreachable: …"
// wrap leaves out. It is built only for the local-socket target (this machine
// should itself be running dejimad); a client pointed at a remote host has its
// own path, runConnectionTroubleshooter.
type daemonDiagnosis struct {
	Cause  string   // one-line "what's actually wrong"
	Steps  []string // ordered remediation, most-likely fix first
	Remote bool     // target is a remote host (changes the render's closing line)
	// Closing overrides the render's trailing hint. The default assumes the fix
	// is run "on the host shell", which is wrong on Windows — there is no host
	// shell there, the commands run right where the client is.
	Closing string
}

// diagnoseRemoteDaemon builds calm, numbered recovery guidance for when the
// client can't reach a REMOTE daemon (DEJIMA_HOST or an active profile pointing
// at a server) — the case a teammate on a phone or a laptop pointed at a server
// hits. Unlike the local diagnosis it can't probe the far side, so it reassures
// first (the work is safe; this is only the connection) and then lists the few
// things the user can actually do from here, ordered most-reassuring /
// most-likely-transient first. Pure string-building (no shelling out), so it's
// cheap to compute on the error.
func diagnoseRemoteDaemon(host string) daemonDiagnosis {
	host = strings.TrimSpace(host)
	shown := host
	if shown == "" {
		shown = "the server"
	}
	reinstall := "curl -fsSL https://dejima.tech/install-client.sh | bash"
	if runtime.GOOS == "windows" {
		reinstall = "irm https://dejima.tech/install-client.ps1 | iex"
	}
	return daemonDiagnosis{
		Remote: true,
		Cause: "can't reach " + shown + " right now — your islands and agents are safe on the server; " +
			"this is just the connection between here and there.",
		Steps: compactSteps([]string{
			nonDefaultPortHint(host),
			"it's often a brief blip (the server can restart after an update) — this retries automatically, so give it ~15s.",
			"check you're on the tailnet:  tailscale status   (the server should be listed) · tailscale ping " + pingTarget(host),
			"refresh this client if it won't recover:  " + reinstall,
			"still stuck? the server may be down — ask the operator, or check the host.",
		}),
	}
}

// nonDefaultPortHint fires when the configured port is not the daemon's, which
// in practice means a typo.
//
// An operator spent a stretch of an evening on a host that "wasn't answering"
// because their saved profile read :7373 rather than :7273 — a transposition,
// invisible in a line they had read a dozen times, and indistinguishable from a
// down server in every message we showed them. Their other machine worked, which
// made it look like the SERVER was refusing this one.
//
// The check is deliberately narrow: it fires only on a port that is not the
// default, and it does not claim the port is wrong — a deliberate non-default
// port is legitimate. It just puts the two numbers next to each other, which is
// all it takes to see a transposition.
//
// Empty when the port IS the default, and compactSteps drops empty entries, so
// the common case is unchanged.
func nonDefaultPortHint(host string) string {
	_, port, err := net.SplitHostPort(hostPortOf(host))
	if err != nil || port == "" || port == defaultDaemonTCPPort {
		return ""
	}
	return "this profile uses port " + port + " — the daemon's default is " +
		defaultDaemonTCPPort + ". If you did not choose " + port + " deliberately, that is the likeliest cause."
}

// hostPortOf normalises a host for SplitHostPort: a bare host with no port has
// none to compare, and a URL form needs its scheme removed first.
func hostPortOf(host string) string {
	h := strings.TrimSpace(host)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	return strings.TrimSuffix(h, "/")
}

// pingTarget renders the bare host (no :port) for a `tailscale ping` hint,
// falling back to a placeholder when the target is unknown.
func pingTarget(host string) string {
	if strings.TrimSpace(host) == "" {
		return "<server>"
	}
	return hostOnly(host)
}

// diagnoseLocalDaemon classifies a local daemon-connection failure by probing
// the socket file and the host service manager, then maps it to concrete fix
// steps. It is read-only but does shell out (service.Detect), so callers should
// compute it once when the error occurs — never on every render frame.
func diagnoseLocalDaemon() daemonDiagnosis {
	// Windows can't host dejimad at all — the generic advice below ("run
	// dejimad --foreground", "dejima service install") names binaries and
	// service managers that don't exist there, sending the user to fix
	// something unfixable. Answer the real question instead: where should the
	// daemon live?
	if runtime.GOOS == "windows" {
		return diagnosisWindowsClient()
	}

	sockPath := "~/.dejima/dejimad.sock"
	sockMissing, permDenied := false, false
	if p, err := paths.SocketPath(); err == nil {
		sockPath = p
		if _, statErr := os.Stat(p); statErr != nil {
			switch {
			case errors.Is(statErr, fs.ErrNotExist):
				sockMissing = true
			case errors.Is(statErr, fs.ErrPermission):
				permDenied = true
			}
		}
	}

	sup := service.Detect()

	switch {
	case permDenied:
		return daemonDiagnosis{
			Cause: "the daemon socket exists but this account can't open it — it's likely owned by another user (dejimad was installed under sudo, or a different login runs it).",
			Steps: compactSteps([]string{
				"see who owns it:  ls -l " + sockPath,
				"then run dejima as that user, or reinstall as this one:  dejima service install",
				"full check:  dejima doctor",
			}),
		}
	case sockMissing:
		return diagnosisNotRunning(sup, sockPath)
	default:
		// Socket present (or unknowable) but the dial still failed: connection
		// refused / timeout / a stale socket → dejimad stopped or crashed.
		return diagnosisStopped(sup)
	}
}

// diagnosisWindowsClient is the local-target diagnosis on Windows, where
// "local" can never work: dejimad needs a Unix host with Docker (scripts/setup.sh
// refuses anything but Darwin/Linux, and internal/service only implements
// launchd + systemd), so the socket this client is looking for will never appear.
//
// The steps are ordered by what most users actually want. WSL2 comes first
// because it is a genuinely local answer — a real Linux kernel with a real
// Docker on this same machine — and `dejima wsl setup` provisions it end to
// end. Pointing at an existing server is second; both beat "install a daemon
// here," which is impossible.
func diagnosisWindowsClient() daemonDiagnosis {
	cause := "Windows can't run the Dejima daemon — dejimad needs a Unix host with Docker, so there's no local socket to connect to. " +
		"You want either a daemon in WSL2 (local, on this machine) or a server to point at."
	steps := []string{
		"set up a local host in WSL2:  dejima wsl setup   (installs Docker + dejimad in a WSL2 distro and connects to it)",
	}
	if wsl.Available() {
		// WSL is already installed, so the setup step is a much shorter trip —
		// say so, since "set up WSL2" otherwise reads as a big-ticket detour.
		steps[0] = "set up a local host in WSL2 (WSL is already installed here):  dejima wsl setup"
	}
	steps = append(steps,
		"or point at an existing server:  dejima profile add <name> <host>:7273   (then `dejima profile switch <name>`)",
		"or, in the TUI:  press [s] → Connection target",
		"joining someone else's server? paste their invite:  dejima join <invite>",
	)
	return daemonDiagnosis{
		Cause:   cause,
		Steps:   compactSteps(steps),
		Closing: "press q to quit, then run one of the above in PowerShell",
	}
}

// diagnosisNotRunning covers the socket-doesn't-exist case: dejimad was never
// started. The remedy forks on whether a service manager already has it
// registered (so it's crash-looping or merely not loaded) vs. nothing installed
// at all (the clicked-past-onboarding case).
func diagnosisNotRunning(sup service.Supervision, sockPath string) daemonDiagnosis {
	switch {
	case sup.Managed:
		// A manager has it loaded, yet there's no socket: it's failing to come up.
		return daemonDiagnosis{
			Cause: "dejimad is registered as a service but its socket never appeared — it looks like it's failing to start.",
			Steps: compactSteps([]string{
				restartHint(sup),
				logHint(),
				"full check:  dejima doctor",
			}),
		}
	case sup.Mode != "none" && sup.Concern != "":
		// Installed (plist/unit present) but not loaded — Detect() already wrote
		// the exact remediation command into Concern.
		return daemonDiagnosis{
			Cause: "dejimad is installed but not loaded: " + sup.Summary + ".",
			Steps: compactSteps([]string{
				sup.Concern,
				logHint(),
			}),
		}
	default:
		// Nothing installed — most likely onboarding was skipped past the
		// install step.
		return daemonDiagnosis{
			Cause: "dejimad isn't running — its control socket (" + sockPath + ") doesn't exist yet. The daemon was probably never installed (a skipped onboarding step).",
			Steps: compactSteps([]string{
				"start it now in this terminal:  dejimad --foreground",
				installHint(),
				"or re-run guided setup:  dejima onboard",
			}),
		}
	}
}

// diagnosisStopped covers the socket-exists-but-refused case: dejimad was up and
// has since stopped or crashed, leaving a stale socket.
func diagnosisStopped(sup service.Supervision) daemonDiagnosis {
	return daemonDiagnosis{
		Cause: "dejimad's socket is there but nothing is answering — the daemon has stopped or crashed (the socket is stale).",
		Steps: compactSteps([]string{
			restartHint(sup),
			logHint(),
			"full check:  dejima doctor",
		}),
	}
}

// restartHint returns the right "bring it back" command for how dejimad is
// supervised. An unmanaged/unknown arrangement was hand-run, so there's nothing
// to restart — run it directly or install it.
func restartHint(sup service.Supervision) string {
	switch sup.Mode {
	case "launchd-system":
		return "restart it:  sudo dejima service restart --system"
	case "systemd-user":
		return "restart it:  dejima service restart   (or: systemctl --user restart dejimad)"
	case "launchd-gui", "launchd-user":
		return "restart it:  dejima service restart"
	default:
		return "start it:  dejimad --foreground   (or install it so it's supervised: dejima service install)"
	}
}

// installHint returns the install command, noting the headless-Mac system
// variant where it matters.
func installHint() string {
	if runtime.GOOS == "darwin" {
		return "install as a service:  dejima service install   (headless Mac: sudo dejima service install --system)"
	}
	return "install as a service:  dejima service install"
}

// logHint points at where the supervised daemon's output lands, per OS. Empty on
// platforms where we don't manage logging (so callers drop it).
func logHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "check the log:  tail -n 50 ~/Library/Logs/dejima/dejimad.err.log"
	case "linux":
		return "check the log:  journalctl --user -u dejimad -n 50 --no-pager"
	default:
		return ""
	}
}

// compactSteps drops empty entries (logHint is "" on some platforms).
func compactSteps(steps []string) []string {
	out := steps[:0:0]
	for _, s := range steps {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// printLocalDaemonHelp writes the diagnosis to stderr in the CLI troubleshooter
// style — called after a *local* command fails to reach the daemon.
func printLocalDaemonHelp(d daemonDiagnosis) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, bold("dejimad isn't reachable"))
	fmt.Fprintln(os.Stderr, "  "+d.Cause)
	if len(d.Steps) > 0 {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  Try:")
		for _, s := range d.Steps {
			fmt.Fprintln(os.Stderr, "    • "+s)
		}
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  More: dejima doctor")
}

// reportSetupIncomplete prints, after a setup flow that left no reachable LOCAL
// daemon, the same classified diagnosis the daemon-unreachable help uses —
// framed as "setup isn't finished." Stderr, so it survives a piped stdout.
func reportSetupIncomplete() {
	d := diagnoseLocalDaemon()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, bold("Setup isn't finished — dejimad isn't reachable yet."))
	fmt.Fprintln(os.Stderr, "  "+d.Cause)
	if len(d.Steps) > 0 {
		fmt.Fprintln(os.Stderr, "  Finish setup, then re-run `dejima onboard`:")
		for _, s := range d.Steps {
			fmt.Fprintln(os.Stderr, "    • "+s)
		}
	}
}

// renderDaemonHelp formats the diagnosis for the TUI's island pane — shown in
// place of the bare "(daemon unreachable?)" line when the local daemon is down.
// offerWSLSetup reports whether the daemon-help panel should offer the one-key
// WSL setup action, given the diagnosis and whether this platform has WSL.
//
// Split out from both callers (renderDaemonHelp and handleKey's `w`) so the
// decision is testable off Windows. Every Windows path in this tree has to be
// reasoned about from a Linux box — `docs/roadmap.md` records that voice already
// shipped broken on Windows for exactly that reason — so the rule is: keep the
// platform check at the edge and make the logic a pure function.
//
// Remote is excluded because a client pointed at someone else's server has no
// business being nudged to build a local host; its diagnosis is about reaching
// that server. The offer belongs only to the local target, which on Windows is
// otherwise a dead end.
func offerWSLSetup(d daemonDiagnosis, hasWSL bool) bool {
	return hasWSL && !d.Remote
}

func renderDaemonHelp(d daemonDiagnosis) string {
	var b strings.Builder
	if d.Remote {
		b.WriteString(styleErrored.Render("Can't reach the server"))
	} else {
		b.WriteString(styleErrored.Render("dejimad isn't reachable"))
	}
	b.WriteString("\n\n")
	b.WriteString(d.Cause)
	if len(d.Steps) > 0 {
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("Try:"))
		b.WriteString("\n")
		for _, s := range d.Steps {
			b.WriteString("  • " + s + "\n")
		}
	}
	// On Windows the first step is `dejima wsl setup`, and there is no host shell
	// to go run it in — the client IS on the machine that needs setting up. So
	// offer it as a keystroke rather than something to retype in PowerShell. The
	// key is handled in handleKey's `w`, which is otherwise "wake an island" and
	// is free here because this panel only renders when there are none.
	if offerWSLSetup(d, wsl.Supported()) {
		b.WriteString("\n" + styleAccent.Render("[w]") +
			styleMuted.Render(" set up WSL2 now — opens `dejima wsl setup` in a new window"))
	}
	switch {
	case d.Closing != "":
		b.WriteString("\n" + styleMuted.Render(d.Closing))
	case d.Remote:
		b.WriteString("\n" + styleMuted.Render("this keeps retrying on its own — press q to quit if you'd rather stop"))
	default:
		b.WriteString("\n" + styleMuted.Render("press q to quit, then run one of the above on the host shell"))
	}
	return b.String()
}

// errWSLProbePending marks a diagnosis built before the distro has been
// inspected. The TUI paints one of these immediately — wsl.Probe shells out and
// can boot a stopped distro, which is seconds the dashboard must not spend
// frozen — and replaces it when the real probe lands.
var errWSLProbePending = errors.New("wsl probe still running")

// diagnoseWSLDaemon builds the diagnosis for a `wsl://<distro>` target.
//
// A wsl:// host is a LOCAL socket tunnel through wsl.exe — `wsl.exe -d <distro>
// -- socat STDIO UNIX-CONNECT:…`. There is no TCP listener, no tailnet, and no
// remote host. Everything diagnoseRemoteDaemon says about those is wrong here,
// and confidently so: an operator whose distro was simply not running was told
// to check `tailscale status`, ping a peer named after their distro, and
// consider that "the server may be down — ask the operator". They are the
// operator, the server is on their own machine, and the one command that would
// have fixed it was on neither list.
//
// The CLI troubleshooter learned this after a real report (see troubleshootWSL,
// which this function now backs). The TUI's offline panel never did, so the same
// operator got the right answer from `dejima wsl status` and the wrong one from
// the dashboard they were already looking at. Both surfaces derive from here now
// so they cannot drift apart again.
//
// rep is nil when the distro has not been inspected yet — a probe shells out to
// wsl.exe and can BOOT a stopped distro, so the TUI paints this first and
// refines it when the probe lands. Nil is rendered as "checking", never as a
// finding: an unasked question is not an answer, and this file already learned
// that once in CredentialMountReport.Known.
func diagnoseWSLDaemon(distro string, rep *wsl.Report, probeErr error) daemonDiagnosis {
	if strings.TrimSpace(distro) == "" {
		distro = wsl.DefaultDistro
	}
	d := daemonDiagnosis{
		// NOT Remote. The fix is on this machine, which is what Remote gates:
		// the closing line, the panel title, and the `w` → `dejima wsl setup`
		// shortcut. Routing a WSL target through the remote path took that
		// keystroke away from the one operator it was built for.
		Remote:  false,
		Closing: "these run right here — the distro is on this machine.",
	}
	head := "the daemon lives in the WSL distro " + quoteDistro(distro) +
		" and isn't answering. Your islands and their work are on that distro's " +
		"disk and are unaffected — this is the tunnel between here and there. " +
		"Tailscale and TCP are not involved in a wsl:// connection."

	switch {
	case errors.Is(probeErr, errWSLProbePending):
		// Painted before the probe returns. Says only what is true without one.
		//
		// An explicit sentinel rather than "both arguments nil": "I have not
		// looked yet" and "I have nothing to tell you" are different states, and
		// encoding the first as an absence is how it gets rendered as the second.
		d.Cause = head
		d.Steps = []string{
			"checking the distro now — this can take a few seconds if it was asleep.",
			"in the meantime:  dejima wsl status",
		}
		return d
	case rep == nil && probeErr == nil && !wsl.Supported():
		// A wsl:// profile on a machine that cannot have WSL. Naming it beats
		// probing something that cannot exist.
		//
		// Gated on having learned NOTHING, not on the platform alone: a caller
		// holding a real report has better information than this shortcut, and
		// deferring to it keeps every branch below reachable off Windows. A
		// diagnosis whose interesting half can only run on the one platform CI
		// does not have is a diagnosis nothing checks.
		d.Cause = "this profile points at a WSL distro (" + quoteDistro(distro) +
			"), but WSL exists only on Windows — so this target can never connect from here."
		d.Steps = []string{
			"switch to a different profile:  dejima profile switch <name>   (list them: dejima profile ls)",
			"or point at the host directly:  DEJIMA_HOST=<host:port>",
		}
		return d
	case probeErr != nil:
		d.Cause = head
		d.Steps = []string{
			"couldn't inspect the distro just now (" + probeErr.Error() + ").",
			"check WSL itself is healthy:  wsl -l -v",
			"then re-run setup, which is idempotent:  dejima wsl setup",
		}
		return d
	case rep == nil:
		d.Cause = head
		d.Steps = []string{
			"checking the distro now — this can take a few seconds if it was asleep.",
			"in the meantime:  dejima wsl status",
		}
		return d
	}

	d.Cause = head
	switch {
	case !rep.Exists:
		d.Cause = "the WSL distro " + quoteDistro(distro) + " does not exist on this machine, " +
			"so there is nothing to connect to. Nothing is lost — it was never created."
		d.Steps = []string{
			"create it:  dejima wsl setup",
			"see what distros you do have:  wsl -l -v",
		}
	case rep.Version == 1:
		// WSL1 has no real kernel, so no Docker. Setup cannot repair this one.
		d.Cause = "the distro " + quoteDistro(distro) + " is WSL version 1, which has no real " +
			"kernel and therefore no Docker — the daemon cannot run there."
		d.Steps = []string{
			"convert it:  wsl --set-version " + distro + " 2   (this can take a while)",
			"then:  dejima wsl setup",
		}
	case !rep.HasSocat:
		d.Cause = "socat is missing inside " + quoteDistro(distro) + ". socat IS the tunnel, so " +
			"nothing can reach the daemon even when it is running perfectly well."
		d.Steps = []string{
			"install it by re-running setup (idempotent — it installs only what is missing):  dejima wsl setup",
		}
	case !rep.HasDejima:
		d.Cause = "the dejimad binary is not installed inside " + quoteDistro(distro) + "."
		d.Steps = []string{"dejima wsl setup"}
	case !rep.HasDocker:
		d.Cause = "Docker is not usable inside " + quoteDistro(distro) + ". The daemon needs it to " +
			"run islands, so it will not come up without it."
		d.Steps = []string{
			"dejima wsl setup",
			"if setup reports Docker installed but not answering, the distro may need a restart:  wsl -t " + distro,
		}
	case !rep.SocketUp:
		// Everything is installed; the daemon simply is not running. This is the
		// common case after a reboot, and the one the tailnet advice buried.
		d.Cause = "the distro " + quoteDistro(distro) + " has socat, dejimad and Docker — everything " +
			"is installed. The daemon just isn't running, which is the usual state after a reboot."
		d.Steps = []string{
			"start it:  dejima wsl start",
			"if that fails, its log is inside the distro:  wsl -d " + distro + " -- tail -40 ~/.dejima/dejimad.log",
		}
	default:
		// Probe says the socket is there and yet the dial failed. Do not pretend
		// to know which; say exactly that, because a confident wrong cause here
		// is what this whole function exists to stop.
		d.Cause = "the distro " + quoteDistro(distro) + " looks healthy — socat, dejimad, Docker and " +
			"the daemon socket are all present — so the dial failed for a reason this check cannot see."
		d.Steps = []string{
			"try again; a distro that was asleep can lose the first knock while it boots.",
			"read the daemon's own log:  wsl -d " + distro + " -- tail -40 ~/.dejima/dejimad.log",
			"restart it:  dejima wsl start",
		}
	}
	return d
}

// quoteDistro renders a distro name for prose.
func quoteDistro(d string) string { return "\"" + d + "\"" }
