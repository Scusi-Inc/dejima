package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultScriptTimeout   = 30 * time.Second
	defaultScriptMaxOutput = 1 << 20 // 1 MiB captured output
	// scriptWaitDelay is the grace after a timeout kill for I/O to drain before
	// the pipes are force-closed (so a child holding the pipe can't block Run).
	scriptWaitDelay = 1 * time.Second
)

// ScriptAdapter runs a user-authored executable in Dir (e.g.
// ~/.dejima/capabilities/) addressed by basename. The directory is the curated
// allowlist: the island can't write to it, and each target must pass strict
// trust checks (regular file, owned by the daemon user, not group/world-
// writable, executable) before it runs. Args arrive as a JSON object on stdin;
// nothing from the request reaches a shell.
type ScriptAdapter struct {
	Dir       string
	Timeout   time.Duration // 0 → defaultScriptTimeout
	MaxOutput int64         // 0 → defaultScriptMaxOutput
}

func (a *ScriptAdapter) Name() string { return "script" }

func (a *ScriptAdapter) Execute(ctx context.Context, req Request) (Result, error) {
	// Defense in depth: the grant layer validated the target, but never trust a
	// path component you're about to filepath.Join. Reject separators/traversal.
	if req.Target == "" || strings.ContainsAny(req.Target, `/\`) || strings.Contains(req.Target, "..") {
		return Result{}, ErrTargetNotFound
	}
	path := filepath.Join(a.Dir, req.Target)
	// Lstat, not Stat: a symlink is not a regular file and is rejected below, so
	// a target can't point out of the curated dir.
	info, err := os.Lstat(path)
	if err != nil {
		return Result{}, ErrTargetNotFound
	}
	if err := trustScript(info); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrTargetUntrusted, err)
	}

	payload, err := json.Marshal(req.Args)
	if err != nil {
		return Result{}, fmt.Errorf("encode args: %w", err)
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
	cmd := exec.CommandContext(rctx, path)
	// On timeout the target is killed, but a child it spawned can inherit the
	// output pipe and keep Run() blocked. WaitDelay bounds that: shortly after the
	// deadline the pipes are force-closed and Run returns, so a hung grandchild
	// can't hold the broker hostage.
	cmd.WaitDelay = scriptWaitDelay
	cmd.Stdin = bytes.NewReader(payload)
	// A minimal, fixed environment — the island's identity, never its env.
	cmd.Env = []string{
		"DEJIMA_CAP_ISLAND=" + req.Island,
		"DEJIMA_CAP_AGENT=" + req.Agent,
		"DEJIMA_CAP_TARGET=" + req.Target,
		"PATH=/usr/bin:/bin",
	}
	outBuf := &limitWriter{w: &bytes.Buffer{}, n: max}
	cmd.Stdout = outBuf
	cmd.Stderr = outBuf // merge: one bounded capture for both streams

	runErr := cmd.Run()
	if rctx.Err() == context.DeadlineExceeded {
		return Result{}, ErrTimeout
	}
	res := Result{Output: outBuf.String()}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode() // ran, non-zero — the caller's concern, not an adapter error
		return res, nil
	}
	if runErr != nil {
		return res, fmt.Errorf("run %q: %w", req.Target, runErr)
	}
	return res, nil
}

// trustScript enforces the curation boundary: only a regular, owner-only-
// writable, executable file owned by the daemon user may run.
func trustScript(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file (symlinks and directories are rejected)")
	}
	mode := info.Mode().Perm()
	if mode&0o022 != 0 {
		return fmt.Errorf("group/world-writable (%#o) — chmod go-w", mode)
	}
	if mode&0o100 == 0 {
		return errors.New("not executable — chmod u+x")
	}
	return ownedByDaemon(info)
}

// limitWriter captures at most n bytes (thread-safe: exec copies stdout and
// stderr from separate goroutines into the same writer).
type limitWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
	n  int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n <= 0 {
		return len(p), nil // at the cap; swallow the rest, report success
	}
	if int64(len(p)) > l.n {
		_, _ = l.w.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

func (l *limitWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

var _ io.Writer = (*limitWriter)(nil)
