package main

import (
	"os/exec"
	"runtime"
)

// checkHostTmux reports whether tmux is installed on THIS machine.
//
// The island image bundles tmux, which makes it easy to assume the host does
// not need it. It does: the TUI opens agent and shell windows by shelling out
// to host tmux (tui_window.go), and `dejima term attach` starts a host pty
// under it. With tmux missing, that surfaces as a raw
//
//	server error: pty start: exec: "tmux": executable file not found in $PATH
//
// which names no remedy and does not say whose machine is at fault. An
// operator hit exactly this on a fresh macOS install and reported being unable
// to tell whether it was Dejima's problem or his: the installer does not
// install tmux and, until now, doctor never looked for it.
//
// Checked locally rather than on the daemon host because the TUI's own tmux
// usage is client-side. In the common local setup they are the same machine;
// when they differ, a host-terminal failure on the far side is a separate
// thing this cannot see, and the detail text says so rather than implying
// full coverage.
func checkHostTmux(r *doctorReport) {
	path, err := exec.LookPath("tmux")
	level, detail, fix := tmuxStatus(path, err, runtime.GOOS)
	r.add("System", "tmux", level, detail, fix)
}

// tmuxStatus classifies the lookup. Pure so the decision table is testable
// without installing or removing a binary.
func tmuxStatus(path string, err error, goos string) (level, detail, fix string) {
	if err == nil && path != "" {
		return "OK", path + " — agent windows and host terminals can start", ""
	}
	return "FAIL",
		"not installed on this machine — opening an agent window or host terminal fails with " +
			`pty start: exec: "tmux": executable file not found in $PATH`,
		"install it: " + tmuxInstallCmd(goos) +
			". The island image bundles tmux, but the client needs its own to open windows"
}

// tmuxInstallCmd is the platform's install line. Named per-OS rather than
// given generically because "install tmux" is the part the operator already
// knew; the command is the part they wanted.
func tmuxInstallCmd(goos string) string {
	switch goos {
	case "darwin":
		return "`brew install tmux`"
	case "windows":
		return "`wsl -- sudo apt install -y tmux` (dejima's windows client runs through WSL)"
	default:
		return "`sudo apt install -y tmux` (or your distro's equivalent)"
	}
}
