// Package service handles installing dejimad as a host service (launchd on
// macOS, systemd user units on Linux). The CLI's `dejima service install`
// command is a thin wrapper over this package.
package service

import (
	"fmt"
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
	Install(binaryPath string) error
	Uninstall() error
	Status() (string, error)
}

// New returns a Manager appropriate for the current OS.
func New() (Manager, error) {
	switch runtime.GOOS {
	case "darwin":
		return &launchdManager{}, nil
	case "linux":
		return &systemdManager{}, nil
	default:
		return nil, fmt.Errorf("dejima service install: unsupported OS %q", runtime.GOOS)
	}
}
