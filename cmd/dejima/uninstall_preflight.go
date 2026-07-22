package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// daemonProcessNames are the process names whose presence in this process's
// ancestry means we are running INSIDE something the daemon supervises.
var daemonProcessNames = map[string]bool{"dejimad": true}

// insideDaemonJob reports whether this process descends from dejimad, and names
// the ancestor if so.
//
// Why it matters: `launchctl bootout` blocks until launchd has torn the job
// down, which means waiting for every process launchd associates with it. The
// daemon spawns host-terminal tmux servers, so a shell opened through a host
// terminal is part of the job. Running the uninstall from there makes the wait
// mutual — launchctl waits for the tmux tree, and the tmux tree contains the
// launchctl. The terminal dies and the command wedges, which is exactly the
// "hung after entering the password" report.
//
// Walks PPIDs via ps rather than reading a marker env var: an operator can open
// a nested shell, run tmux themselves, or su, any of which loses an env var but
// keeps the ancestry.
func insideDaemonJob() (ancestor string, inside bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	pid := os.Getpid()
	// Bounded: a corrupt ppid chain must not spin. Real trees are shallow.
	for range 24 {
		ppid, name, ok := parentOf(pid)
		if !ok || ppid <= 1 {
			return "", false
		}
		if daemonProcessNames[name] {
			return name, true
		}
		pid = ppid
	}
	return "", false
}

// parentOf returns pid's parent pid and the parent's command name.
func parentOf(pid int) (ppid int, name string, ok bool) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || ppid <= 0 {
		return 0, "", false
	}
	nameOut, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		return ppid, "", true
	}
	// comm may be a full path (Linux) or a bare name (macOS).
	n := strings.TrimSpace(string(nameOut))
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	return ppid, n, true
}

// preflightNotInsideDaemon refuses an uninstall that would deadlock against
// itself, explaining where to run it instead. force skips the check for an
// operator who knows better (or for a shape the ancestry walk misreads).
func preflightNotInsideDaemon(force bool) error {
	ancestor, inside := insideDaemonJob()
	if !inside {
		return nil
	}
	if force {
		fmt.Fprintf(os.Stderr, "warning: this shell descends from %s; teardown may stall (--force given, continuing)\n", ancestor)
		return nil
	}
	return fmt.Errorf("this shell is running inside the daemon you're uninstalling (a %s child — a host terminal?).\n"+
		"Tearing down the service waits for every process it owns, including this one, so it would hang.\n\n"+
		"  Run the uninstall from a normal terminal on the host instead — e.g. an SSH session\n"+
		"  or Terminal.app, not a dejima host terminal.\n\n"+
		"Pass --force to override this check.", ancestor)
}
