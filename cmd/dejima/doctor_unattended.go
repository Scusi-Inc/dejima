package main

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
)

// A Dejima host is bought to be ignored, and that is exactly what makes this
// worth checking.
//
// The incident: a Mac mini serving a remote client had Docker die on Aug 18 and
// nobody noticed until Sep 1, when someone tried to make an island. Two weeks of
// downtime on a machine working perfectly in every other respect.
//
// The mechanism is specific to macOS hosts and is not obvious. Docker Desktop is
// a GUI application: its engine runs inside a logged-in user's session, and its
// socket lives in that user's home. A Mac that reboots to a LOGIN SCREEN has no
// session, therefore no Docker, therefore no islands — and nothing says so,
// because the daemon is up, the network is up, and the machine answers.
//
// Both halves are required and neither is sufficient. Automatic login without
// Docker's start-at-login gives a desktop with no engine. Start-at-login without
// automatic login gives a setting that never fires. Checking only one would be
// worse than checking neither, because it would report a reassuring OK.

// tristate distinguishes "we looked and it is off" from "we could not tell".
// Collapsing those is how a check reports a clean bill of health it never
// established — the failure this file exists to prevent, reintroduced inside it.
type tristate int

const (
	triUnknown tristate = iota
	triYes
	triNo
)

// unattendedHostVerdict decides what to report from the two facts. Pure, so the
// decision is testable on any platform — the Mac-only part is the LOOKING, and
// the part worth getting right is the JUDGING.
func unattendedHostVerdict(dockerAutoStart, autoLogin tristate) (status, detail, fix string) {
	const remedy = "Docker Desktop → Settings → General → \"Start Docker Desktop when you sign in\", " +
		"AND System Settings → Users & Groups → Automatic login. " +
		"Both are needed: auto-login without Docker's setting gives a desktop with no engine, " +
		"and Docker's setting without auto-login never fires because nobody signs in."

	switch {
	case dockerAutoStart == triYes && autoLogin == triYes:
		return "OK", "this host restarts into a working Docker unattended", ""
	case dockerAutoStart == triNo && autoLogin == triNo:
		return "WARN", "a reboot will leave this host with no Docker and no sign-in — islands stay down until someone notices", remedy
	case dockerAutoStart == triNo:
		return "WARN", "Docker won't start itself after a reboot, so islands stay down until someone opens it", remedy
	case autoLogin == triNo:
		return "WARN", "this Mac reboots to a login screen, so Docker never starts and islands stay down", remedy
	default:
		// Something was unreadable. Say so plainly rather than passing: an
		// unverified claim of health is what let the original outage run two
		// weeks.
		return "INFO", "couldn't confirm this host recovers from a reboot unattended", remedy
	}
}

func checkUnattendedHost(ctx context.Context, r *doctorReport) {
	if runtime.GOOS != "darwin" {
		return
	}
	// Only meaningful on the machine that RUNS the islands. Asking it about a
	// laptop that merely drives a daemon would be advice about the wrong Mac.
	if _, remote := daemonElsewhere(); remote {
		return
	}
	status, detail, fix := unattendedHostVerdict(dockerAutoStartSetting(ctx), autoLoginSetting(ctx))
	r.add("System", "unattended restart", status, detail, fix)
}

// autoLoginSetting reads the system-wide automatic-login user. Absent or empty
// means the Mac boots to a login screen.
func autoLoginSetting(ctx context.Context) tristate {
	out, err := exec.CommandContext(ctx, "defaults", "read",
		"/Library/Preferences/com.apple.loginwindow", "autoLoginUser").Output()
	if err != nil {
		// `defaults` exits non-zero when the key is absent, which is the normal
		// way "automatic login is off" presents.
		return triNo
	}
	if strings.TrimSpace(string(out)) == "" {
		return triNo
	}
	return triYes
}

// dockerAutoStartSetting reads Docker Desktop's own settings store.
//
// The filename moved between versions (settings.json → settings-store.json) and
// the key's capitalisation has varied, so this checks both names and matches the
// key case-insensitively. When neither file is readable it returns UNKNOWN
// rather than guessing — a wrong "off" here nags someone whose host is fine, and
// a wrong "on" is the silent failure this whole check exists to catch.
func dockerAutoStartSetting(ctx context.Context) tristate {
	home, err := exec.CommandContext(ctx, "sh", "-c", "echo $HOME").Output()
	if err != nil {
		return triUnknown
	}
	base := strings.TrimSpace(string(home)) + "/Library/Group Containers/group.com.docker/"
	for _, name := range []string{"settings-store.json", "settings.json"} {
		b, readErr := exec.CommandContext(ctx, "cat", base+name).Output()
		if readErr != nil {
			continue
		}
		return autoStartFromSettings(string(b))
	}
	return triUnknown
}

// autoStartFromSettings pulls the autostart flag out of Docker's settings JSON.
// Split out so the parsing is testable without a Docker install.
func autoStartFromSettings(body string) tristate {
	lower := strings.ToLower(body)
	i := strings.Index(lower, "\"autostart\"")
	if i < 0 {
		return triUnknown
	}
	rest := lower[i+len("\"autostart\""):]
	if c := strings.Index(rest, ","); c >= 0 {
		rest = rest[:c]
	}
	switch {
	case strings.Contains(rest, "true"):
		return triYes
	case strings.Contains(rest, "false"):
		return triNo
	}
	return triUnknown
}
