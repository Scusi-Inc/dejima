//go:build windows

package main

import (
	"context"
	"os"
	"time"
)

// sizingFd is the fd to read the terminal size from. On Windows the console
// size lives on the *output* handle — GetConsoleScreenBufferInfo (which
// term.GetSize uses) fails on the stdin handle callers pass — so redirect to
// stdout.
func sizingFd(_ int) int { return int(os.Stdout.Fd()) }

// watchTerminalResize forwards terminal-size changes for the life of ctx.
// Windows has no SIGWINCH, so we poll the console dimensions a few times a
// second and emit a resize only when they actually change. Cheap, and good
// enough for an interactive session — the agent reflows on the next event.
func watchTerminalResize(ctx context.Context, fd int, send func(rows, cols uint16)) {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		lastRows, lastCols, _ := terminalSize(fd)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rows, cols, err := terminalSize(fd)
				if err != nil {
					continue
				}
				if rows != lastRows || cols != lastCols {
					lastRows, lastCols = rows, cols
					send(rows, cols)
				}
			}
		}
	}()
}
