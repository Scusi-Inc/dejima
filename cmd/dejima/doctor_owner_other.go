//go:build !unix

package main

// rootOwnedStateFiles is a no-op off Unix — there's no comparable uid model, and
// the daemon host is always Unix. Keeps the Windows/other client build green.
func rootOwnedStateFiles(string) []string { return nil }
