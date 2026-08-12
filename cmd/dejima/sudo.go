package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/term"
)

// Homebrew cask installs need root partway through, not up front: the Docker
// cask links its CLI (`docker`, `com.docker.cli`, the compose plugin, the zsh
// completions) into /usr/local, which on Apple Silicon sits outside Homebrew's
// /opt/homebrew prefix and is root-owned. So `brew install --cask docker-desktop` runs
// fine for a minute, moves the app, and only THEN shells out to sudo.
//
// That mid-run prompt arrives on a terminal Homebrew is already capturing for
// its own `==>` output, and the observed result on a fresh mini is bad: the
// password is echoed in the clear and the run appears to wedge. We can't fix
// Homebrew's plumbing, but we can make sure it never has to prompt — sudo's
// timestamp is per-tty and our child inherits our tty, so priming it here means
// brew's own sudo calls find a warm ticket and go straight through.
//
// primeSudo returns a stop func that must be called when the privileged work is
// done. It is safe to call more than once.
func primeSudo(reason string) (stop func()) {
	noop := func() {}
	// Already root (e.g. the whole wizard was run under sudo): nothing to prime.
	if os.Geteuid() == 0 {
		return noop
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return noop
	}
	// No tty means no place to prompt. Let the child fail its own way rather
	// than blocking a scripted run on a password that can never be typed.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return noop
	}

	if !sudoTimestampWarm() {
		fmt.Println()
		fmt.Printf("  %s needs your macOS password partway through (Homebrew links\n", reason)
		fmt.Println("  binaries into /usr/local, which is root-owned). Asking now, once, so")
		fmt.Println("  the installer doesn't stop to ask in the middle of its own output:")
		if err := execInteractive("sudo", "-v"); err != nil {
			// Declined, mistyped, or not a sudoer. Not fatal — brew will ask on
			// its own terms, which is exactly the path this avoids but still
			// beats refusing to continue.
			fmt.Printf("  ⚠ couldn't pre-authorize sudo (%v) — the installer may prompt mid-run\n", err)
			return noop
		}
	}

	// The ticket expires (5 minutes by default) and a cask download can outlast
	// it, so hold it open for the duration of the install.
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(sudoKeepaliveInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = sudoTimestampWarm()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
		})
	}
}

// sudoKeepaliveInterval is well inside sudo's default 5-minute timestamp
// timeout. A var so tests don't have to wait on it.
var sudoKeepaliveInterval = 60 * time.Second

// sudoTimestampWarm refreshes the sudo timestamp without prompting, reporting
// whether it's currently valid. `-n` makes sudo fail rather than ask, so this
// never reads a byte of stdin — but it still gets os.Stdin so sudo resolves the
// same tty our child will use (macOS enables tty_tickets, which key the
// timestamp by terminal).
func sudoTimestampWarm() bool {
	c := exec.Command("sudo", "-n", "-v")
	c.Stdin = os.Stdin
	c.Stdout = io.Discard
	c.Stderr = io.Discard
	return c.Run() == nil
}
