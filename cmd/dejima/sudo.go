package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
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
	// No CONTROLLING TERMINAL means no place to prompt. Let the child fail its
	// own way rather than blocking a scripted run on a password that can never
	// be typed.
	//
	// Deliberately not `term.IsTerminal(os.Stdin)`, which is what this used to
	// be: piped stdin does not mean nobody is present. `curl -fsSL … | bash`
	// makes stdin a pipe while a person watches from the keyboard, and reading
	// that as "no human" is what made the shell installer skip its own priming
	// and let Homebrew's sudo take the operator's password with echo on (#341).
	tty := openTTY()
	if tty == nil {
		return noop
	}

	if !sudoTimestampWarm(tty) {
		fmt.Println()
		fmt.Printf("  %s needs your macOS password partway through (Homebrew links\n", reason)
		fmt.Println("  binaries into /usr/local, which is root-owned). Asking now, once, so")
		fmt.Println("  the installer doesn't stop to ask in the middle of its own output:")
		if err := sudoValidate(tty); err != nil {
			// Declined, mistyped, or not a sudoer. Not fatal — brew will ask on
			// its own terms, which is exactly the path this avoids but still
			// beats refusing to continue.
			fmt.Printf("  ⚠ couldn't pre-authorize sudo (%v) — the installer may prompt mid-run\n", err)
			tty.Close()
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
				_ = sudoTimestampWarm(tty)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
			tty.Close()
		})
	}
}

// openTTY returns the controlling terminal, or nil when there isn't one.
//
// A var so tests can substitute it: the case that matters most — a human at the
// keyboard while stdin is a pipe — cannot be produced by running a test the
// ordinary way, and CI has no controlling terminal at all.
var openTTY = func() *os.File {
	// O_RDWR because sudo wants to both prompt and read on it. Fails with ENXIO
	// when the process has no controlling terminal, and ENOENT on Windows —
	// both of which correctly mean "nobody to ask".
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	return f
}

// sudoValidate runs `sudo -v`, prompting on the terminal rather than on
// whatever stdin happens to be.
func sudoValidate(tty *os.File) error {
	c := exec.Command("sudo", "-v")
	c.Stdin = tty
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// sudoKeepaliveInterval is well inside sudo's default 5-minute timestamp
// timeout. A var so tests don't have to wait on it.
var sudoKeepaliveInterval = 60 * time.Second

// sudoTimestampWarm refreshes the sudo timestamp without prompting, reporting
// whether it's currently valid. `-n` makes sudo fail rather than ask, so this
// never reads a byte of it — but it still gets the terminal so sudo resolves
// the same tty our child will use (macOS enables tty_tickets, which key the
// timestamp by terminal). Handing it a pipe instead would warm a ticket keyed
// to something Homebrew's own sudo never looks up.
func sudoTimestampWarm(tty *os.File) bool {
	c := exec.Command("sudo", "-n", "-v")
	c.Stdin = tty
	c.Stdout = io.Discard
	c.Stderr = io.Discard
	return c.Run() == nil
}
