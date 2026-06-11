//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// sizingFd is the fd to read the terminal size from. On Unix the size is a
// property of the tty itself, so any tty fd (the stdin fd callers pass) works.
func sizingFd(stdinFd int) int { return stdinFd }

// watchTerminalResize forwards terminal-size changes for the life of ctx.
// On Unix we listen for SIGWINCH, which the kernel raises whenever the
// controlling terminal's window size changes.
func watchTerminalResize(ctx context.Context, fd int, send func(rows, cols uint16)) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(winch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-winch:
				if rows, cols, err := terminalSize(fd); err == nil {
					send(rows, cols)
				}
			}
		}
	}()
}
