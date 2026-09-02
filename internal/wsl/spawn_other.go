//go:build !windows

package wsl

import "os/exec"

// isolateConsole is a no-op off Windows. The console-sharing problem it solves
// is specific to spawning wsl.exe from a console process; see spawn_windows.go
// for what goes wrong and how it was identified.
func isolateConsole(cmd *exec.Cmd) {}
