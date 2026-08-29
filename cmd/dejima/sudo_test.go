package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// primeSudo must never prompt from a test binary.
//
// The previous version of this guard asserted only that primeSudo returned a
// non-nil stop func, on the stated premise that "under `go test` stdin isn't a
// terminal". That premise stopped being true when the #341 fix moved the check
// from stdin to /dev/tty — a test process has a real controlling terminal, so
// primeSudo began running `sudo -v` against it. The suite printed "[sudo]
// password for …" onto whatever terminal was attached, consumed input from it,
// and STILL REPORTED PASS, because the only thing asserted was the return value.
//
// So this asserts the behaviour instead of the signature: sudo is never reached.
func TestPrimeSudoNeverPromptsFromATestBinary(t *testing.T) {
	called := false
	orig := sudoValidateFn
	sudoValidateFn = func(*os.File) error {
		called = true
		return nil
	}
	t.Cleanup(func() { sudoValidateFn = orig })

	stop := primeSudo("Installing Something")
	if called {
		t.Error("primeSudo ran `sudo -v` from a test binary — that prompts for a password " +
			"on the developer's (or an attached operator's) terminal, mid-suite, with " +
			"nothing naming which test is asking")
	}
	if stop == nil {
		t.Fatal("primeSudo returned a nil stop func — callers defer it unconditionally")
	}
	// Idempotent: the wiring calls stop() on both the success and error paths of
	// the install, and a double close would panic.
	stop()
	stop()
}

// TestCaskInstallsPrimeSudo is the regression guard for the fresh-mini failure:
// `brew install --cask docker-desktop` asks for a password partway through (it links
// into root-owned /usr/local), that prompt lands on a terminal Homebrew is
// capturing, and the password echoes in the clear before the run wedges.
// Priming sudo first is what prevents it — so any NEW cask install has to do the
// same, or it reintroduces the bug.
func TestCaskInstallsPrimeSudo(t *testing.T) {
	root := repoRoot(t)
	entries, err := filepath.Glob(filepath.Join(root, "cmd", "dejima", "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	caskRe := regexp.MustCompile(`"--cask"`)

	found := 0
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			if !caskRe.MatchString(line) {
				continue
			}
			found++
			// primeSudo must be established just above the exec — close enough
			// that the ticket is warm when brew runs.
			start := i - 5
			if start < 0 {
				start = 0
			}
			window := strings.Join(lines[start:i], "\n")
			if !strings.Contains(window, "primeSudo") {
				t.Errorf("%s:%d installs a Homebrew cask without priming sudo first:\n\t%s\n"+
					"wrap it: stopSudo := primeSudo(...); err := execInteractive(...); stopSudo()",
					filepath.Base(path), i+1, strings.TrimSpace(line))
			}
		}
	}
	if found == 0 {
		t.Fatal("no cask installs found — this guard is scanning the wrong place")
	}
}

// The curl installer (install.sh → `make setup` → scripts/setup.sh) never
// touches the Go wizard, so the shell path needs the same priming or the
// one-line install still reproduces the bug. Only actual invocations count —
// a cask name quoted inside an info/hint string isn't running anything.
func TestShellCaskInstallsPrimeSudo(t *testing.T) {
	root := repoRoot(t)
	scripts, err := filepath.Glob(filepath.Join(root, "scripts", "*.sh"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	scripts = append(scripts, filepath.Join(root, "install.sh"))
	invokeRe := regexp.MustCompile(`^\s*brew install .*--cask`)
	heredocRe := regexp.MustCompile(`<<-?\s*([A-Za-z_'"][A-Za-z0-9_'"]*)`)

	for _, path := range scripts {
		body, err := os.ReadFile(path)
		if err != nil {
			continue // install.sh is the only non-glob entry; tolerate a rename
		}
		lines := strings.Split(string(body), "\n")
		heredoc := "" // terminator while inside one; "" when not
		for i, line := range lines {
			// Text emitted into a heredoc is advice we print, not a command we
			// run — gen-homebrew-formula.sh's caveats say "brew install --cask
			// docker-desktop" without ever executing it.
			if heredoc != "" {
				if strings.TrimSpace(line) == heredoc {
					heredoc = ""
				}
				continue
			}
			if m := heredocRe.FindStringSubmatch(line); m != nil {
				heredoc = strings.Trim(m[1], `'"`)
				continue
			}
			if !invokeRe.MatchString(line) {
				continue
			}
			start := i - 5
			if start < 0 {
				start = 0
			}
			if !strings.Contains(strings.Join(lines[start:i], "\n"), "prime_sudo") {
				t.Errorf("%s:%d installs a Homebrew cask without priming sudo first:\n\t%s\n"+
					"call prime_sudo \"…\" just above it (and stop_sudo_keepalive after)",
					filepath.Base(path), i+1, strings.TrimSpace(line))
			}
		}
	}
}
