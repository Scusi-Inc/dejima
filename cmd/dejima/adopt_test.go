package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/reposrc"
)

// gitInTest runs a git command in dir, failing the test on error. Used to build
// real repos so classifyCandidate exercises the actual reposrc.GetStatus path.
func gitInTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	// Deterministic identity so commits don't depend on the test box's git config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestAdoptCommandRegistered(t *testing.T) {
	// The adopt command must be wired into the root tree (also satisfies the
	// coverage gate's "adopt" reference).
	root := newRootCmd()
	var found bool
	for _, c := range root.Commands() {
		if c.Name() == "adopt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("`dejima adopt` is not registered on the root command")
	}
}

func TestClassifyCandidateRejectsURL(t *testing.T) {
	if _, err := classifyCandidate("git@github.com:o/r.git"); err == nil {
		t.Fatal("expected a git URL to be rejected for adoption")
	}
	if _, err := classifyCandidate("https://github.com/o/r.git"); err == nil {
		t.Fatal("expected an https URL to be rejected for adoption")
	}
}

func TestClassifyCandidateNonDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := classifyCandidate(f); err == nil {
		t.Fatal("expected a non-directory path to be rejected")
	}
	if _, err := classifyCandidate(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected a missing path to be rejected")
	}
}

func TestClassifyCandidateNonGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	cand, err := classifyCandidate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cand.shape != shapeNonGit {
		t.Fatalf("shape = %v, want shapeNonGit", cand.shape)
	}
	if cand.name == "" {
		t.Fatal("expected a derived island name")
	}
}

func TestClassifyCandidateGitClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInTest(t, dir, "init")
	gitInTest(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, dir, "add", "-A")
	gitInTest(t, dir, "commit", "-m", "init")
	// No upstream is configured, so GetStatus reports ahead=0; a clean tree with
	// an origin should classify as a plain git-remote repo.
	cand, err := classifyCandidate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cand.origin != "https://github.com/o/r.git" {
		t.Fatalf("origin = %q", cand.origin)
	}
	if cand.shape != shapeGitRemote {
		t.Fatalf("shape = %v, want shapeGitRemote", cand.shape)
	}
}

func TestClassifyCandidateGitDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInTest(t, dir, "init")
	gitInTest(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	// An uncommitted file makes the tree dirty → unpushed-work shape.
	if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cand, err := classifyCandidate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cand.shape != shapeGitUnpushed {
		t.Fatalf("shape = %v, want shapeGitUnpushed (dirty tree)", cand.shape)
	}
}

func TestClassifyCandidateGitNoRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInTest(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, dir, "add", "-A")
	gitInTest(t, dir, "commit", "-m", "init")
	// A repo with no remote has nothing to clone from → must be seeded locally.
	cand, err := classifyCandidate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cand.origin != "" {
		t.Fatalf("origin = %q, want empty", cand.origin)
	}
	if cand.shape != shapeGitUnpushed {
		t.Fatalf("shape = %v, want shapeGitUnpushed (no remote)", cand.shape)
	}
}

func TestGitInitWithCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "code.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gitInitWithCommit(context.Background(), dir); err != nil {
		t.Fatalf("gitInitWithCommit: %v", err)
	}
	// It must now be a real, seedable repo: a .git dir and at least one commit.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("expected .git after init: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("no commit after init: %v\n%s", err, out)
	}
	// And it must reclassify as an adoptable git repo (the non-git → seedable path).
	cand, err := classifyCandidate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cand.shape == shapeNonGit {
		t.Fatal("expected the directory to be a git repo after gitInitWithCommit")
	}
}

func TestGitInitWithCommitEmptyDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	// Even an empty directory should end up with a valid (empty) initial commit so
	// the seed clone has something to clone.
	if err := gitInitWithCommit(context.Background(), dir); err != nil {
		t.Fatalf("gitInitWithCommit(empty): %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("expected an initial commit in an empty repo: %v\n%s", err, out)
	}
}

func TestConfirmDefaults(t *testing.T) {
	// --yes short-circuits regardless of the prompt's default.
	if !adoptConfirm(true, "anything [y/N]: ") {
		t.Fatal("assumeYes should always confirm")
	}
}

func TestConfirmEmptyAnswerHonorsDefault(t *testing.T) {
	// Drive readSingleKey's shared reader with an empty line, then assert the
	// default embedded in the prompt wins.
	orig := stdinReader
	defer func() { stdinReader = orig }()

	setStdin := func(s string) { stdinReader = bufio.NewReader(strings.NewReader(s)) }

	setStdin("\n")
	if !adoptConfirm(false, "go? [Y/n]: ") {
		t.Fatal("empty answer to a [Y/n] prompt should default to yes")
	}
	setStdin("\n")
	if adoptConfirm(false, "go? [y/N]: ") {
		t.Fatal("empty answer to a [y/N] prompt should default to no")
	}
	setStdin("y\n")
	if !adoptConfirm(false, "go? [y/N]: ") {
		t.Fatal("explicit y should confirm")
	}
	setStdin("n\n")
	if adoptConfirm(false, "go? [Y/n]: ") {
		t.Fatal("explicit n should decline")
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandTilde("~/code"); got != filepath.Join(home, "code") {
		t.Errorf("expandTilde(~/code) = %q", got)
	}
	if got := expandTilde("/abs/path"); got != "/abs/path" {
		t.Errorf("expandTilde left an absolute path alone: %q", got)
	}
}

func TestDescribeShape(t *testing.T) {
	cases := []struct {
		cand adoptCandidate
		want string
	}{
		{adoptCandidate{shape: shapeNonGit}, "not a git repo"},
		{adoptCandidate{shape: shapeGitRemote, origin: "https://x/y.git"}, "git repo (origin"},
		{adoptCandidate{shape: shapeGitUnpushed, origin: "", status: reposrc.Status{Dirty: true}}, "local work"},
	}
	for _, tc := range cases {
		if got := describeShape(tc.cand); !strings.Contains(got, tc.want) {
			t.Errorf("describeShape(%v) = %q, want substring %q", tc.cand.shape, got, tc.want)
		}
	}
}
