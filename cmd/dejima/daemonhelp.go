package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/service"
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
			"it's often a brief blip (the server can restart after an update) — this retries automatically, so give it ~15s.",
			"check you're on the tailnet:  tailscale status   (the server should be listed) · tailscale ping " + pingTarget(host),
			"refresh this client if it won't recover:  " + reinstall,
			"still stuck? the server may be down — ask the operator, or check the host.",
		}),
	}
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
	if d.Remote {
		b.WriteString("\n" + styleMuted.Render("this keeps retrying on its own — press q to quit if you'd rather stop"))
	} else {
		b.WriteString("\n" + styleMuted.Render("press q to quit, then run one of the above on the host shell"))
	}
	return b.String()
}
