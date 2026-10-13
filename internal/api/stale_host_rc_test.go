package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The rc-line edit a local install offers.
//
// An operator reinstalled Dejima locally on a Mac that had been a CLIENT. The
// installer detected the leftover `export DEJIMA_HOST=100.89.51.27:7273` in
// ~/.zshenv, said so, and said "Nothing here edits it" — and then everything
// after it failed against the old server: doctor reported the daemon
// unreachable, `dejima` could not connect, and creating an island timed out
// posting to a machine on someone else's network. The warning was correct and
// useless, which is the shape that strands people.
//
// It now OFFERS to comment the line. That means editing a file the operator
// wrote by hand, so the edit has to be provably conservative: comment rather
// than delete, back up first, and leave every other line untouched.
//
// THE EXPRESSION IS READ OUT OF setup.sh, not copied here. A test carrying its
// own copy of a sed passes forever while the shipped script does something else
// — which is the guards-need-controls failure with the subject swapped for a
// lookalike.
func staleHostSed(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "setup.sh"))
	if err != nil {
		t.Fatalf("read setup.sh: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*sed -i\.tmp ('[^']*')\s+"\$RC_WITH_HOST"`)
	m := re.FindSubmatch(b)
	if m == nil {
		t.Fatal("no stale-DEJIMA_HOST sed in scripts/setup.sh — the subject this " +
			"test exercises is gone, so every assertion below would pass vacuously")
	}
	return strings.Trim(string(m[1]), "'")
}

func runSed(t *testing.T, expr, path string) {
	t.Helper()
	out, err := exec.Command("sed", "-i.tmp", expr, path).CombinedOutput()
	if err != nil {
		t.Fatalf("sed: %v: %s", err, out)
	}
	_ = os.Remove(path + ".tmp")
}

func TestStaleHostEditIsConservative(t *testing.T) {
	expr := staleHostSed(t)
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshenv")
	original := `# my shell setup
export PATH="$HOME/bin:$PATH"
export DEJIMA_HOST=100.89.51.27:7273
export EDITOR=vim
alias ll='ls -la'
`
	if err := os.WriteFile(rc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	runSed(t, expr, rc)
	got, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)

	if !strings.Contains(s, "# disabled by dejima local install: export DEJIMA_HOST=") {
		t.Errorf("the DEJIMA_HOST line was not commented and labelled:\n%s", s)
	}
	// NOT DELETED. Someone wrote that address; they must be able to find it.
	if !strings.Contains(s, "100.89.51.27:7273") {
		t.Errorf("the address was destroyed rather than disabled:\n%s", s)
	}
	// THE PART THAT MATTERS MOST. A sed that eats an unrelated line is worse
	// than the bug it fixes: the operator loses shell config that had nothing to
	// do with Dejima, from a command they said yes to once.
	for _, want := range []string{
		"# my shell setup",
		`export PATH="$HOME/bin:$PATH"`,
		"export EDITOR=vim",
		"alias ll='ls -la'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("mangled or removed an unrelated line %q:\n%s", want, s)
		}
	}
	if a, b := strings.Count(original, "\n"), strings.Count(s, "\n"); a != b {
		t.Errorf("line count changed %d -> %d", a, b)
	}
}

// Running it twice must not comment the comment — an installer is re-run all
// the time, and a line that accumulates prefixes is a line nobody can restore.
func TestStaleHostEditIsIdempotent(t *testing.T) {
	expr := staleHostSed(t)
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshenv")
	if err := os.WriteFile(rc, []byte("export DEJIMA_HOST=old:7273\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSed(t, expr, rc)
	runSed(t, expr, rc)
	got, _ := os.ReadFile(rc)
	if n := strings.Count(string(got), "disabled by dejima local install"); n != 1 {
		t.Errorf("comment applied %d times:\n%s", n, got)
	}
}

// A line the operator already commented out is not ours to touch.
func TestStaleHostEditLeavesCommentedLinesAlone(t *testing.T) {
	expr := staleHostSed(t)
	dir := t.TempDir()
	rc := filepath.Join(dir, ".bashrc")
	original := "# export DEJIMA_HOST=old:7273\nexport EDITOR=nano\n"
	if err := os.WriteFile(rc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	runSed(t, expr, rc)
	got, _ := os.ReadFile(rc)
	if string(got) != original {
		t.Errorf("modified a line that was already disabled:\nwant %q\ngot  %q", original, got)
	}
}
