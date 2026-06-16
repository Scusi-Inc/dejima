package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ShortcutsAdapter runs a macOS Apple Shortcut by name via the `shortcuts` CLI.
// The user's Shortcuts library is the curated allowlist: the operator grants a
// Shortcut name, and the brain can invoke only granted ones. Args are handed to
// the shortcut as JSON on a temp input file; the shortcut's result is captured
// from stdout. The target is matched by exact name (no path to traverse) and
// only ever appears on argv — never a shell.
//
// Environment caveat (see docs/capability-broker-spec.md): the `shortcuts` CLI
// talks to a GUI-session helper. A shortcut LOOKS UP fine from a background /
// daemon context (a missing name returns "Couldn't find shortcut"), but whether
// a system LaunchDaemon can actually EXECUTE one — versus needing the user's
// logged-in aqua session — must be verified live with a real Shortcut.
type ShortcutsAdapter struct {
	Timeout   time.Duration // 0 → defaultScriptTimeout
	MaxOutput int64         // 0 → defaultScriptMaxOutput
}

func (a *ShortcutsAdapter) Name() string { return "shortcuts" }

func (a *ShortcutsAdapter) Execute(ctx context.Context, req Request) (Result, error) {
	payload, err := json.Marshal(req.Args)
	if err != nil {
		return Result{}, fmt.Errorf("encode args: %w", err)
	}
	in, err := os.CreateTemp("", "dejima-cap-in-*.json")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(in.Name())
	if _, err := in.Write(payload); err != nil {
		in.Close()
		return Result{}, err
	}
	if err := in.Close(); err != nil {
		return Result{}, err
	}

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = defaultScriptTimeout
	}
	max := a.MaxOutput
	if max <= 0 {
		max = defaultScriptMaxOutput
	}

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, "/usr/bin/shortcuts", "run", req.Target, "--input-path", in.Name())
	cmd.WaitDelay = scriptWaitDelay
	out := &limitWriter{w: &bytes.Buffer{}, n: max}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if rctx.Err() == context.DeadlineExceeded {
		return Result{}, ErrTimeout
	}
	if runErr == nil {
		return Result{Output: out.String(), ExitCode: 0}, nil
	}

	// `shortcuts run` exits non-zero for a missing shortcut and for a shortcut
	// that ran but failed. Classify not-found (fail closed, distinct) by its
	// message; otherwise treat a clean non-zero exit as a Result (the shortcut
	// ran, the caller sees the exit code), and anything else as an adapter error.
	msg := strings.TrimSpace(stderr.String())
	if strings.Contains(msg, "find shortcut") {
		return Result{}, ErrTargetNotFound
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		combined := strings.TrimSpace(out.String() + "\n" + msg)
		return Result{Output: combined, ExitCode: exitErr.ExitCode()}, nil
	}
	return Result{}, fmt.Errorf("run shortcut %q: %w: %s", req.Target, runErr, msg)
}
