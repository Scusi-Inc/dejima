package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/paths"
)

// uninstallClient removes Dejima from a CLIENT-ONLY machine — the CLI and the
// saved server connection — WITHOUT contacting a daemon or touching any island.
//
// This is the counterpart to the full `dejima uninstall`, which assumes a local
// daemon and fails at "is the daemon running?" on a laptop or Windows box that
// only drives a remote server. Here there's nothing to tear down on the wire:
// just this machine's binary and its connection config.
//
// The binary is the awkward part cross-platform (a package manager owns it, or
// a running .exe can't remove itself), so we remove what we safely can and print
// the exact command for the rest rather than pretending.
func uninstallClient(yes bool) error {
	// Two things identify a client install: the connection config and the binary.
	cfgPath, _ := clientcfg.Path()
	root, _ := paths.Root()
	hostJSON := filepath.Join(root, "host.json") // the DEJIMA_HOST record from install

	fmt.Println("Client uninstall — removes the Dejima CLI and its saved server connection from")
	fmt.Println("THIS machine only. Your server, its daemon, and every island are untouched.")
	fmt.Println()
	fmt.Println("Will remove this machine's connection config:")
	for _, p := range []string{cfgPath, hostJSON} {
		if p != "" && fileExists(p) {
			fmt.Printf("  %s\n", p)
		}
	}

	if !yes {
		fmt.Print("\nProceed? [y/N]: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			fmt.Println("aborted.")
			return nil
		}
	}

	// Remove the connection config (best-effort; a missing file is fine).
	for _, p := range []string{cfgPath, hostJSON} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  warning: couldn't remove %s: %v\n", p, err)
		}
	}
	fmt.Println("removed the saved server connection.")

	// The binary: name the right removal for how it was installed, because we
	// can't reliably self-delete (a package manager owns it, or on Windows a
	// running .exe is locked).
	fmt.Println()
	fmt.Println("To remove the `dejima` binary itself, use whichever installed it:")
	fmt.Println("  npm:        npm uninstall -g dejima")
	if runtime.GOOS == "darwin" {
		fmt.Println("  Homebrew:   brew uninstall dejima")
	}
	if runtime.GOOS == "windows" {
		fmt.Println("  PowerShell: Remove-Item (Get-Command dejima).Source   (close other dejima windows first)")
	} else {
		if exe, err := os.Executable(); err == nil {
			fmt.Printf("  standalone: rm %s\n", exe)
		}
	}
	fmt.Println()
	fmt.Println("Nothing else on this machine is Dejima's — you're done.")
	return nil
}

// fileExists reports whether path is an existing file (client uninstall only
// lists paths that are actually there).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
