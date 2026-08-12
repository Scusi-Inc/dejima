//go:build !windows

package main

// prepareConsoleOutput is a no-op off Windows. A Unix terminal already defers
// end-of-line wrap and needs no opt-in to interpret VT sequences, so there is no
// output-handle mode to set — only Windows consoles have that wart. See the
// windows build of this file for what it fixes and why.
func prepareConsoleOutput() (restore func(), err error) { return func() {}, nil }
