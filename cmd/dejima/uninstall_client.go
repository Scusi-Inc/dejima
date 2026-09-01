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
	if printShellPathLeftovers() {
		return nil
	}
	fmt.Println()
	fmt.Println("Nothing else on this machine is Dejima's — you're done.")
	return nil
}

// printShellPathLeftovers names the PATH line install-client.sh appends to a
// shell startup file, and reports whether it found any.
//
// This is the same defect as the Windows one below, arriving on Unix later: the
// installer now drops the binary in ~/.local/bin and, when that is not already
// on PATH, appends
//
//	# added by the dejima installer
//	export PATH="$HOME/.local/bin:$PATH"
//
// to ~/.zshrc, ~/.bash_profile or ~/.bashrc. Removing the binary leaves that
// line pointing at a directory Dejima no longer occupies, and the all-clear
// would be false in exactly the way it was on Windows.
//
// Printed, not edited: a shell startup file is a thing people hand-tune, and an
// uninstaller rewriting one unasked is worse than an uninstaller that tells you
// precisely which line to delete. The installer's marker comment makes it exact
// — this reports only files that actually contain it, so a machine that never
// needed the PATH edit still gets the honest all-clear.
func printShellPathLeftovers() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	const marker = "added by the dejima installer"

	var found []string
	for _, name := range []string{".zshrc", ".bash_profile", ".bashrc", ".profile"} {
		p := filepath.Join(home, name)
		b, readErr := os.ReadFile(p)
		if readErr == nil && strings.Contains(string(b), marker) {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return false
	}

	// Each file is named ONCE, in the actionable form. An earlier version also
	// listed the paths on their own above this — which mutation testing showed
	// was redundant rather than thorough: deleting the list changed nothing the
	// operator could act on, because the command below already carries the path.
	fmt.Println()
	fmt.Println("One thing the installer wrote that the step above does NOT remove: a PATH")
	fmt.Printf("entry in your shell startup file. Delete the two lines marked %q, or:\n", marker)
	fmt.Println()
	for _, p := range found {
		fmt.Printf("  sed -i.bak '/%s/,+1d' %s\n", marker, p)
	}
	fmt.Println()
	fmt.Println("Harmless if left — it points at a directory Dejima no longer occupies. Takes")
	fmt.Println("effect in a new shell.")
	return true
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
