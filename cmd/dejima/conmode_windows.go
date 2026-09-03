//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// prepareConsoleOutput puts the Windows console's OUTPUT handle into the mode a
// curses-style byte stream requires, and returns a func restoring the previous
// mode. Always returns a non-nil restore func, so callers can defer it blindly.
//
// term.MakeRaw only configures the INPUT handle — nothing here ever touched the
// output side, which left DISABLE_NEWLINE_AUTO_RETURN off and caused the
// left-column character smearing that Ctrl-L "fixed" temporarily:
//
//   - DISABLE_NEWLINE_AUTO_RETURN gives the console DEFERRED end-of-line wrap,
//     the behavior every Unix terminal has and every curses app assumes. Without
//     it, writing the last column immediately moves the cursor to column 0 of the
//     next line; tmux expects the cursor to stay put, so its next write lands a
//     line low at column 0 and the line's leading characters trail down the left
//     edge. Full-width lines are what trigger it, which is why an agent TUI
//     drawing box borders and rules smeared constantly while a plain shell —
//     rarely writing to the last column — looked fine.
//   - ENABLE_VIRTUAL_TERMINAL_PROCESSING makes the console interpret the ANSI/VT
//     sequences in that stream at all. ConPTY usually has it on already; setting
//     it is belt-and-braces for a console handle that doesn't.
//
// A non-console stdout (redirected to a file or pipe) fails GetConsoleMode; that
// is not an error worth surfacing, since there is no terminal to configure.
func prepareConsoleOutput() (restore func(), err error) {
	noop := func() {}
	h := windows.Handle(os.Stdout.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return noop, err
	}
	want := old | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.DISABLE_NEWLINE_AUTO_RETURN
	if want == old {
		return noop, nil
	}
	if err := windows.SetConsoleMode(h, want); err != nil {
		return noop, err
	}
	// Restore exactly what was there: this process may share the console with the
	// shell that launched it, so leaving our flags behind would alter its output.
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}
