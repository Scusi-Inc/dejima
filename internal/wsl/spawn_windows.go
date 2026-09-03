//go:build windows

package wsl

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps a spawned wsl.exe away from the console this process is
// drawing a TUI on.
//
// THE BUG THIS FIXES. wsl.exe is a console application. Launched from a console
// process with no creation flags, it ATTACHES TO THE PARENT'S CONSOLE and can
// leave its input mode altered. The dashboard polls the daemon on a tick, and on
// a WSL host every poll is another wsl.exe sharing the operator's console — so
// the terminal is being reached into every few seconds while a person is typing
// at it.
//
// The symptom, reported from a real Windows machine: arrow keys stopped working
// and UP OPENED THE AUDIT LEDGER. Up is ESC [ A, and with the console's input
// mode disturbed the sequence arrived as three separate keypresses, so the
// trailing "A" landed on the audit binding. Esc did nothing, because a bare Esc
// and the start of a sequence are the same byte. j/k worked, because plain
// letters carry no escape sequence.
//
// WHAT MAKES THIS CERTAIN RATHER THAN PLAUSIBLE: the same client, on the same
// machine, in the same terminal, is fine when pointed at a remote daemon over
// TCP. It only breaks against a WSL host. The one thing that changes is whether
// wsl.exe is being spawned — which is this code.
//
// CREATE_NO_WINDOW gives the child no console of its own and stops it inheriting
// ours. It does not affect the transport: the socat dial talks over pipes, and
// every other call here captures stdout and stderr explicitly.
const createNoWindow = 0x08000000

// isolateConsole must be called on EVERY wsl.exe command before it starts. One
// missed call is enough to reintroduce this: the console only has to be
// disturbed once for the keystrokes in flight to be mangled.
func isolateConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
