// Package service handles installing dejimad as a host service (launchd on
// macOS, systemd user units on Linux). The CLI's `dejima service install`
// command is a thin wrapper over this package.
package service

import (
	"fmt"
	"os"
	"runtime"
)

// Action is install, uninstall, status, etc.
type Action string

const (
	ActionInstall   Action = "install"
	ActionUninstall Action = "uninstall"
	ActionStatus    Action = "status"
)

// Manager is the OS-specific service manager.
type Manager interface {
	// Install registers dejimad with the host service manager. args are the
	// extra command-line arguments baked into the service definition (e.g.
	// ["--tcp", ":7273"] to expose the Tailscale-pinned TCP listener).
	Install(binaryPath string, args []string) error
	Uninstall() error
	Status() (string, error)
	// Restart bounces the running daemon (e.g. after `make install` or a
	// release upgrade replaced the binary).
	Restart() error
}

// New returns a Manager appropriate for the current OS. With system=true on
// macOS it manages a system-wide LaunchDaemon instead of a per-user
// LaunchAgent — that's the reboot-durable option for headless Macs, where the
// gui launchd domain (which LaunchAgents need) never exists.
func New(system bool) (Manager, error) {
	switch runtime.GOOS {
	case "darwin":
		if system {
			return &launchdSystemManager{}, nil
		}
		// Auto-detect a system-domain install: if the system LaunchDaemon
		// plist exists and there's no per-user LaunchAgent, manage the system
		// domain so `dejima service restart`/`status`/`uninstall` work without
		// the user remembering --system.
		if _, err := os.Stat(launchdSystemPlist); err == nil {
			agent := &launchdManager{}
			agentPlist, perr := agent.plistPath()
			if perr != nil {
				return &launchdSystemManager{}, nil
			}
			if _, err := os.Stat(agentPlist); err != nil {
				fmt.Fprintln(os.Stderr, "note: dejimad is installed as a system LaunchDaemon — managing the system domain (sudo may prompt)")
				return &launchdSystemManager{}, nil
			}
		}
		return &launchdManager{}, nil
	case "linux":
		if system {
			return nil, fmt.Errorf("--system is macOS-only; on a headless Linux box, user units " +
				"survive reboots after `loginctl enable-linger $USER`")
		}
		return &systemdManager{}, nil
	default:
		return nil, fmt.Errorf("dejima service install: unsupported OS %q", runtime.GOOS)
	}
}
