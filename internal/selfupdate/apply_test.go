package selfupdate

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo makes a temp dir, git-inits it, and lands one commit so the tree is
// clean. Skips if git isn't available.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/aoos/dejima\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestApplySourceDryRunListsPlanNoExec(t *testing.T) {
	dir := gitRepo(t)
	var ran [][]string
	rec := func(ctx context.Context, d, name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}
	var out bytes.Buffer
	if err := ApplySource(context.Background(), dir, false, &out, rec); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("dry run executed commands: %v", ran)
	}
	s := out.String()
	for _, want := range []string{"git pull --ff-only", "make install", "dejima service restart", "dry run"} {
		if !bytes.Contains([]byte(s), []byte(want)) {
			t.Errorf("dry-run output missing %q:\n%s", want, s)
		}
	}
}

func TestApplySourceExecuteRunsPlan(t *testing.T) {
	dir := gitRepo(t)
	var ran [][]string
	rec := func(ctx context.Context, d, name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}
	if err := ApplySource(context.Background(), dir, true, &bytes.Buffer{}, rec); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 3 || ran[0][0] != "git" || ran[1][0] != "make" || ran[2][0] != "dejima" {
		t.Fatalf("executed plan = %v, want git/make/dejima", ran)
	}
}

func TestApplySourceRefusesDirtyTree(t *testing.T) {
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := func(ctx context.Context, d, name string, args ...string) error { return nil }
	err := ApplySource(context.Background(), dir, true, &bytes.Buffer{}, rec)
	if err == nil {
		t.Fatal("expected refusal on dirty tree")
	}
}

func TestApplySourceRejectsNonRepo(t *testing.T) {
	rec := func(ctx context.Context, d, name string, args ...string) error { return nil }
	if err := ApplySource(context.Background(), t.TempDir(), false, &bytes.Buffer{}, rec); err == nil {
		t.Fatal("expected error for non-git dir")
	}
}

func TestFindCheckout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/aoos/dejima\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindCheckout(nested)
	if err != nil {
		t.Fatal(err)
	}
	// macOS /tmp symlinks to /private/tmp; compare resolved paths.
	gotR, _ := filepath.EvalSymlinks(got)
	dirR, _ := filepath.EvalSymlinks(dir)
	if gotR != dirR {
		t.Fatalf("FindCheckout = %q, want %q", gotR, dirR)
	}

	// A tree with no dejima go.mod errors.
	if _, err := FindCheckout(t.TempDir()); err == nil {
		t.Fatal("expected error when no checkout found")
	}
}
