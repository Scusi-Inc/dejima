package service

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// Supervision describes how (and whether) dejimad is supervised by the host
// service manager, and whether that arrangement survives a reboot. It answers
// the question `dejima doctor` couldn't before: not just "is the daemon up?"
// but "will it come back on its own?".
type Supervision struct {
	Managed       bool   // a service manager currently has dejimad loaded
	Mode          string // launchd-system | launchd-gui | launchd-user | systemd-user | none | unknown
	RebootDurable bool   // comes back after a reboot with no interactive login
	Summary       string // human one-liner for the report row
	Concern       string // non-empty → a reboot-survival caveat worth a WARN
}

// Detect inspects the host service manager for a dejimad supervision
// arrangement. Best-effort: never errors; returns Mode "none" when nothing is
// found and "unknown" on an unsupported OS.
func Detect() Supervision {
	switch runtime.GOOS {
	case "darwin":
		return detectLaunchd()
	case "linux":
		return detectSystemd()
	default:
		return Supervision{Mode: "unknown", Summary: "supervision detection unsupported on " + runtime.GOOS}
	}
}

// FixArgv returns the command `dejima doctor --fix` should run to remediate this
// supervision Concern, or nil if there's nothing to auto-fix. selfBin is the path
// to the dejima binary, used for fixes that re-run a dejima subcommand (so all the
// install/sudo logic is reused rather than reimplemented). The mapping is kept
// here, next to the classification, so the report layer stays declarative.
func (s Supervision) FixArgv(selfBin string) []string {
	if s.Concern == "" {
		return nil
	}
	switch s.Mode {
	case "systemd-user":
		// enabled-but-no-linger and active-but-not-enabled both surface here;
		// distinguish by the Concern text the classifier set.
		if strings.Contains(s.Concern, "enable-linger") && !strings.Contains(s.Concern, "systemctl --user enable") {
			return []string{"loginctl", "enable-linger", currentUser()}
		}
		// active but not enabled: enable first; a re-run then flags linger.
		return []string{"systemctl", "--user", "enable", systemdUnitName}
	case "launchd-system":
		// plist present but not loaded — just (re)load the system daemon.
		return []string{selfBin, "service", "restart", "--system"}
	case "launchd-gui", "launchd-user":
		// login-gated/per-boot supervisor on a headless Mac — reinstall as the
		// boot-durable system LaunchDaemon (reuses service install's sudo path).
		return []string{selfBin, "service", "install", "--system"}
	default:
		return nil
	}
}

func detectSystemd() Supervision {
	isActive := svcRun("systemctl", "--user", "is-active", systemdUnitName)
	isEnabled := svcRun("systemctl", "--user", "is-enabled", systemdUnitName)
	linger := strings.TrimPrefix(svcRun("loginctl", "show-user", currentUser(), "--property=Linger"), "Linger=")
	return classifySystemd(isActive, isEnabled, linger)
}

// classifySystemd is the pure decision for the systemd --user case, split out so
// the reboot-survival logic is testable without shelling out.
func classifySystemd(isActive, isEnabled, linger string) Supervision {
	active := strings.TrimSpace(isActive) == "active"
	enabled := strings.TrimSpace(isEnabled) == "enabled"
	lingerOn := strings.EqualFold(strings.TrimSpace(linger), "yes")
	if !active && !enabled {
		return Supervision{Mode: "none", Summary: "no systemd --user unit for dejimad"}
	}
	s := Supervision{Managed: true, Mode: "systemd-user"}
	switch {
	case enabled && lingerOn:
		s.RebootDurable = true
		s.Summary = "systemd --user unit, enabled with linger — survives reboot"
	case enabled && !lingerOn:
		s.Summary = "systemd --user unit enabled, but linger is off"
		s.Concern = "won't start until you next log in after a reboot — `loginctl enable-linger " + currentUser() + "`"
	default: // active but not enabled
		s.Summary = "systemd --user unit running but not enabled"
		s.Concern = "running now but won't start on boot — `systemctl --user enable " + systemdUnitName + "` and `loginctl enable-linger " + currentUser() + "`"
	}
	return s
}

func detectLaunchd() Supervision {
	uid := currentUID()
	return classifyLaunchd(
		fileExists(launchdSystemPlist),
		svcRun("launchctl", "print", "system/"+launchdLabel) != "",
		svcRun("launchctl", "print", "gui/"+uid+"/"+launchdLabel) != "",
		svcRun("launchctl", "print", "user/"+uid+"/"+launchdLabel) != "",
	)
}

// classifyLaunchd is the pure decision for the launchd case given which plist
// exists and which domains have the label loaded. System domain is the only
// reboot-durable, login-free arrangement; gui needs a desktop login and user is
// per-boot.
func classifyLaunchd(systemPlist, systemLoaded, guiLoaded, userLoaded bool) Supervision {
	switch {
	case systemPlist && systemLoaded:
		return Supervision{Managed: true, Mode: "launchd-system", RebootDurable: true,
			Summary: "launchd system LaunchDaemon — loads at boot, no login needed"}
	case systemPlist && !systemLoaded:
		return Supervision{Mode: "launchd-system",
			Summary: "system LaunchDaemon plist present but not loaded",
			Concern: "plist installed but the daemon isn't loaded — `sudo dejima service restart --system`"}
	case guiLoaded:
		return Supervision{Managed: true, Mode: "launchd-gui",
			Summary: "launchd LaunchAgent (gui domain) — loads at desktop login",
			Concern: "loads only after a desktop login; on a headless Mac it won't start at boot — `sudo dejima service install --system`"}
	case userLoaded:
		return Supervision{Managed: true, Mode: "launchd-user",
			Summary: "launchd LaunchAgent (user domain, headless fallback) — supervised this boot only",
			Concern: "user-domain agent is per-boot; it won't return after a reboot — `sudo dejima service install --system`"}
	default:
		return Supervision{Mode: "none", Summary: "no launchd service for dejimad"}
	}
}

// svcRun runs a command and returns its trimmed stdout, or "" on any failure.
// Captures stdout even on a non-zero exit (is-active/is-enabled report their
// status that way).
func svcRun(name string, args ...string) string {
	out, _ := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out))
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "$USER"
}
