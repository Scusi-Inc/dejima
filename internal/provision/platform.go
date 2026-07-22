package provision

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

// PhasesFor returns the ordered phase list for an OS. macOS and Linux differ in
// what's automatable (macOS has GUI-only System Settings steps; Linux uses a
// package manager and systemd lingering) but share the install-handoff and
// verify tail. An unsupported OS is an error.
func PhasesFor(goos string) ([]Phase, error) {
	switch goos {
	case "darwin":
		return []Phase{
			&systemConfigPhase{},
			&toolingPhase{},
			&shellSSHPhase{},
			&installPhase{},
			&verifyPhase{},
		}, nil
	case "linux":
		return []Phase{
			&toolingPhase{},
			&shellSSHPhase{},
			&lingerPhase{},
			&installPhase{},
			&verifyPhase{},
		}, nil
	default:
		return nil, fmt.Errorf("host provisioning is macOS/Linux only (this is %s)", goos)
	}
}

// detectLinuxPkgMgr returns "apt", "dnf", or "" based on what's on PATH.
func detectLinuxPkgMgr(r Runner) string {
	switch {
	case r.Look("apt-get"):
		return "apt"
	case r.Look("dnf"):
		return "dnf"
	default:
		return ""
	}
}

// isHeadlessMac reports whether this Mac has no aqua (GUI) login session, the
// case where a per-user LaunchAgent never loads and the daemon needs the system
// LaunchDaemon. Best-effort: a missing `launchctl` reads as not-headless.
func isHeadlessMac(r Runner) bool {
	// In a GUI session the Aqua security session is present; `launchctl managername`
	// returns "Aqua". Over SSH with no console login it isn't.
	out, err := r.Run("launchctl", "managername")
	if err != nil {
		return false
	}
	return !strings.Contains(strings.ToLower(out), "aqua")
}

// realUser returns the human account the wizard acts for, recovering the
// pre-sudo user from $SUDO_USER when run under sudo (so files land owned by the
// operator, not root). Mirrors the recovery in internal/service/launchd_system.go.
func realUser() (*user.User, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	if u.Uid == "0" {
		if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
			if real, lerr := user.Lookup(sudoUser); lerr == nil {
				return real, nil
			}
		}
	}
	return u, nil
}
