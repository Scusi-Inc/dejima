package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner executes a command in dir (empty = current dir), streaming its output.
// Injected so apply logic is testable without mutating the host.
type Runner func(ctx context.Context, dir, name string, args ...string) error

// ExecRunner runs commands for real, streaming combined output to out.
func ExecRunner(out io.Writer) Runner {
	return func(ctx context.Context, dir, name string, args ...string) error {
		c := exec.CommandContext(ctx, name, args...)
		c.Dir = dir
		c.Stdout = out
		c.Stderr = out
		return c.Run()
	}
}

// sourceStep is one command in the source-mode update plan.
type sourceStep struct {
	dir  string // "" → run in the process's cwd (e.g. `dejima service restart`)
	name string
	args []string
}

func (s sourceStep) String() string {
	line := s.name + " " + strings.Join(s.args, " ")
	if s.dir != "" {
		line = fmt.Sprintf("(in %s) %s", s.dir, line)
	}
	return strings.TrimSpace(line)
}

// sourcePlan is the fixed source-mode update sequence: fast-forward the checkout,
// reinstall both binaries, restart the service so client+daemon move together.
func sourcePlan(dir string) []sourceStep {
	return []sourceStep{
		{dir, "git", []string{"pull", "--ff-only"}},
		{dir, "make", []string{"install"}},
		{"", "dejima", []string{"service", "restart"}},
	}
}

// SourcePreflight verifies dir is a dejima git checkout with a clean tree (safe
// to fast-forward + reinstall). Shared by ApplySource and the daemon's
// self-update so neither force-merges or clobbers local work.
func SourcePreflight(ctx context.Context, dir string) error {
	if !isGitRepo(dir) {
		return fmt.Errorf("%s is not a git checkout", dir)
	}
	clean, err := gitClean(ctx, dir)
	if err != nil {
		return fmt.Errorf("check working tree: %w", err)
	}
	if !clean {
		return fmt.Errorf("working tree at %s is dirty; commit or stash first (refusing to touch local changes)", dir)
	}
	return nil
}

// ApplySource updates a source install. It preflights (a clean dejima checkout
// that can fast-forward), then either prints the plan (execute=false, the
// default) or runs it. It never force-merges or discards local work: a dirty or
// diverged tree is a hard stop, not something to paper over.
func ApplySource(ctx context.Context, dir string, execute bool, out io.Writer, run Runner) error {
	if err := SourcePreflight(ctx, dir); err != nil {
		return err
	}

	for _, s := range sourcePlan(dir) {
		if !execute {
			fmt.Fprintf(out, "  would run: %s\n", s)
			continue
		}
		fmt.Fprintf(out, "→ %s\n", s)
		if err := run(ctx, s.dir, s.name, s.args...); err != nil {
			return fmt.Errorf("%s failed: %w", s.name, err)
		}
	}
	if !execute {
		fmt.Fprintln(out, "\n(dry run — run `dejima update` to apply)")
	}
	return nil
}

// PrepareSource runs every source-update step EXCEPT the final restart:
// preflight, fast-forward the checkout, reinstall both binaries. The restart is
// deliberately left to the caller because it kills the running daemon — so the
// daemon's self-update can do this part synchronously (and report failures to
// the client) and only background the restart. Returns a wrapped error naming
// the step that failed.
func PrepareSource(ctx context.Context, dir string, out io.Writer, run Runner) error {
	if err := SourcePreflight(ctx, dir); err != nil {
		return err
	}
	for _, s := range sourcePlan(dir) {
		if s.name == "dejima" { // the restart step — caller's responsibility
			continue
		}
		fmt.Fprintf(out, "→ %s\n", s)
		if err := run(ctx, s.dir, s.name, s.args...); err != nil {
			return fmt.Errorf("%s failed: %w", s.name, err)
		}
	}
	return nil
}

// FindCheckout walks up from start looking for the dejima source tree (a go.mod
// declaring this module). Returns an error with guidance if none is found.
func FindCheckout(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if bytes.HasPrefix(bytes.TrimSpace(b), []byte("module github.com/aoos/dejima")) {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no dejima checkout found from %s — run this from your checkout, or pass --source <dir>", start)
		}
		dir = parent
	}
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular()) // dir, or a gitlink file (worktree)
}

func gitClean(ctx context.Context, dir string) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) == 0, nil
}

// ErrReleaseApplyUnsupported marks the not-yet-built release download path, so
// the command can print manual steps instead of failing opaquely.
var ErrReleaseApplyUnsupported = errors.New("automatic apply for release installs isn't supported yet")

// ResolveSourceDir locates the git checkout a source install was built from,
// for recording in InstallMeta. Returns "" when it genuinely cannot be found.
//
// Tries three things in order, because the single cwd-derived lookup this
// replaces was the reason `service install` could silently DESTROY a working
// record: the meta is written whole, so a run from outside the checkout (say
// `sudo dejima service install --system` typed from $HOME) resolved nothing,
// wrote SourceDir:"" over a good value, and left the daemon unable to update
// itself. Preserving a previously recorded checkout is the important part.
func ResolveSourceDir() string {
	if cwd, err := os.Getwd(); err == nil {
		if dir, err := FindCheckout(cwd); err == nil {
			return dir
		}
	}
	// Keep what we already knew rather than erasing it.
	if prev, err := LoadInstallMeta(); err == nil && prev.SourceDir != "" {
		if _, err := os.Stat(filepath.Join(prev.SourceDir, "go.mod")); err == nil {
			return prev.SourceDir
		}
	}
	// The path install.sh clones to, which is where a scripted install lives.
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".dejima-src")
		if _, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil {
			return candidate
		}
	}
	return ""
}
