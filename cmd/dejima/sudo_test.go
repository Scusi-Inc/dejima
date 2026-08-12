package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Under `go test` stdin isn't a terminal, so primeSudo must take the no-op path
// and never try to prompt. That's also the scripted-run guarantee: a wizard run
// with piped stdin must not block on a password nobody can type.
func TestPrimeSudo_NoTTYIsNoop(t *testing.T) {
	stop := primeSudo("Installing Something")
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
