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
		printWindowsLeftovers()
		return nil
	}
	if exe, err := os.Executable(); err == nil {
		fmt.Printf("  standalone: rm %s\n", exe)
	}
	fmt.Println()
	fmt.Println("Nothing else on this machine is Dejima's — you're done.")
	return nil
}

// printWindowsLeftovers names what install-client.ps1 wrote that this command
// cannot remove, because on Windows "nothing else is Dejima's" was simply false.
//
// The PowerShell installer writes three things beyond ~/.dejima: a program
// directory (%LOCALAPPDATA%\dejima, holding dejima.exe plus any .old-* sidecars
// self-update left behind), an entry appended to the User PATH, and — when its
// Tailscale step runs — a User-scope DEJIMA_HOST. The first is a real directory
// no package manager owns, so `npm uninstall` and `brew uninstall` both no-op
// on it and leave a working `dejima` on PATH after a "successful" uninstall.
// The third outranks everything in client.json (see resolveTarget), so leaving
// it behind means the connection this command claims to have removed is still
// in force.
//
// These are printed rather than removed: the running .exe is locked so it
// cannot delete itself, and rewriting a user's PATH unprompted is not something
// an uninstaller should do silently. Naming them precisely is the fix.
func printWindowsLeftovers() {
	// Built with a literal separator, not filepath.Join: this string is pasted
	// into PowerShell by a human, and Join uses the separator of the machine
	// doing the building. That is right in production (this path is Windows-only)
	// but it silently yields "…\AppData\Local/dejima" anywhere else, which is the
	// sort of thing that reads fine in review and looks broken to an operator.
	prefix := os.Getenv("DEJIMA_PREFIX")
	if prefix == "" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			prefix = strings.TrimRight(local, `\/`) + `\dejima`
		} else {
			prefix = `%LOCALAPPDATA%\dejima`
		}
	}

	fmt.Println("  installer:  Remove-Item -Recurse " + prefix)
	fmt.Println("              (close other dejima windows first — a running .exe is locked)")
	fmt.Println()
	fmt.Println("Two more things the PowerShell installer wrote, which the steps above do NOT")
	fmt.Println("remove. Skip them and `dejima` keeps working from a new shell:")
	fmt.Println()
	fmt.Println("  1. Its entry on your User PATH:")
	fmt.Printf("       $p = [Environment]::GetEnvironmentVariable('Path','User')\n")
	fmt.Printf("       $new = ($p -split ';' | Where-Object { $_ -ne '%s' }) -join ';'\n", prefix)
	fmt.Printf("       [Environment]::SetEnvironmentVariable('Path', $new, 'User')\n")
	fmt.Println()
	fmt.Println("  2. DEJIMA_HOST, if you gave the installer a server address. It overrides")
	fmt.Println("     every saved profile, so it outlives the connection removed above:")
	fmt.Println("       [Environment]::SetEnvironmentVariable('DEJIMA_HOST', $null, 'User')")
	fmt.Println()
	fmt.Println("Both need a new PowerShell to take effect. Verify with:  Get-Command dejima -All")
}

// fileExists reports whether path is an existing file (client uninstall only
// lists paths that are actually there).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
