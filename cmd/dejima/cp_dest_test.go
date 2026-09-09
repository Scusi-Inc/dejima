package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/runtime/runtimetest"
)

// `dejima cp <island>:<file> ./` is the form the command's own --help gives, and
// it did not work in either direction. An operator on Windows hit the outbound
// half:
//
//	> dejima cp playbook-internal:/home/dejima/memory.tar.gz ./
//	Error: open ./: is a directory
//
// These tests hold both halves. They keep their own env rather than using
// cliEnv because the inbound assertion needs the RUNTIME FAKE — the destination
// the daemon finally asked docker for is the only thing that separates a file
// that landed where it was asked for from one that landed beside it under a
// temp name, and cliEnv keeps its fake to itself.

func cpEnv(t *testing.T) (*api.Client, *runtimetest.Fake) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_TOKEN", "")
	isolateSecretsBackend(t)
	ledger.ResetDefault()
	fake := runtimetest.New()
	srv := joinBackground(t, api.NewServer(fake, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Setenv("DEJIMA_HOST", ts.URL)
	c, err := api.NewTCPClient(ts.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c, fake
}

// TestCpOutToDirectory: island -> host, destination is a directory. The file
// must land INSIDE it under the source's own name.
func TestCpOutToDirectory(t *testing.T) {
	c, _ := cpEnv(t)
	seedIsland(t, c, "proj")
	dir := t.TempDir()

	// Bare directory, and the trailing-slash form the operator actually typed.
	for _, dst := range []string{dir, dir + string(os.PathSeparator)} {
		if _, err := runCLI(t, "cp", "proj:/home/dejima/memory.tar.gz", dst); err != nil {
			t.Fatalf("cp to %q: %v", dst, err)
		}
		landed := filepath.Join(dir, "memory.tar.gz")
		if _, err := os.Stat(landed); err != nil {
			t.Errorf("cp to %q did not create %s: %v", dst, landed, err)
		}
		if err := os.Remove(landed); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}

// TestCpOutKeepsAnExplicitName is the control for TestCpOutToDirectory. A
// destination that is NOT a directory must be used verbatim — without this,
// hostCpDest could append the source basename unconditionally and both tests
// would still pass.
func TestCpOutKeepsAnExplicitName(t *testing.T) {
	c, _ := cpEnv(t)
	seedIsland(t, c, "proj")
	dst := filepath.Join(t.TempDir(), "renamed.tar.gz")

	if _, err := runCLI(t, "cp", "proj:/home/dejima/memory.tar.gz", dst); err != nil {
		t.Fatalf("cp: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("cp should have written exactly %s: %v", dst, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "memory.tar.gz")); err == nil {
		t.Error("cp treated an explicit filename as a directory")
	}
}

// TestCpInToDirectory: host -> island, destination has a trailing slash.
//
// This is the half that failed SILENTLY. The client used to send
// "/home/dejima/intake/" as the write path; the daemon streams the body to a
// temp file and hands that to `docker cp`, which — given a directory
// destination — names the result after the SOURCE. The source is the daemon's
// temp file, so the bytes arrived as /home/dejima/intake/dejima-cp-1234567 and
// the command exited 0.
func TestCpInToDirectory(t *testing.T) {
	c, fake := cpEnv(t)
	seedIsland(t, c, "proj")
	src := filepath.Join(t.TempDir(), "patch.diff")
	if err := os.WriteFile(src, []byte("diff"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if _, err := runCLI(t, "cp", src, "proj:/home/dejima/intake/"); err != nil {
		t.Fatalf("cp host→island: %v", err)
	}

	dests := fake.CopyDests()
	if len(dests) != 1 {
		t.Fatalf("expected exactly one copy into the container, got %v", dests)
	}
	if dests[0] != "/home/dejima/intake/patch.diff" {
		t.Errorf("file landed at %q, want /home/dejima/intake/patch.diff", dests[0])
	}
	// The invariant underneath the equality, stated so it survives a change to
	// the expected path: the daemon must never be handed a path ending in a
	// separator, because that is the precondition under which docker — not us —
	// picks the filename, and the name it picks is the temp file's.
	//
	// Asserted here rather than by looking for "dejima-cp-" in the destination:
	// the fake does not implement docker's naming rule, so a test for the temp
	// name could never fail and would be a guard with nothing behind it.
	if strings.HasSuffix(dests[0], "/") {
		t.Errorf("daemon was handed a directory (%q) — docker would name the file", dests[0])
	}
}

// TestCpInKeepsAnExplicitName is the control for TestCpInToDirectory: no
// trailing slash means the path names a file, which is also how you create one.
func TestCpInKeepsAnExplicitName(t *testing.T) {
	c, fake := cpEnv(t)
	seedIsland(t, c, "proj")
	src := filepath.Join(t.TempDir(), "patch.diff")
	if err := os.WriteFile(src, []byte("diff"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if _, err := runCLI(t, "cp", src, "proj:/home/dejima/renamed.diff"); err != nil {
		t.Fatalf("cp host→island: %v", err)
	}
	dests := fake.CopyDests()
	if len(dests) != 1 || dests[0] != "/home/dejima/renamed.diff" {
		t.Errorf("explicit destination not honoured verbatim: %v", dests)
	}
}

// TestCpHelpExamplesRun: the examples in `dejima cp --help` are the reason this
// bug reached an operator — they were correct about the intent and wrong about
// the implementation for as long as both existed. Assert the shapes the help
// text promises are the shapes the code handles, so the two cannot drift apart
// again silently.
func TestCpHelpExamplesRun(t *testing.T) {
	c, _ := cpEnv(t)
	seedIsland(t, c, "proj")

	long := newCpCmd().Long
	for _, want := range []string{"foo:/workspace/README.md ./", "foo:/home/dejima/intake/"} {
		if !strings.Contains(long, want) {
			t.Fatalf("help no longer shows %q — this test is guarding an example that moved", want)
		}
	}

	// The outbound example, run for real against a temp cwd.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if _, err := runCLI(t, "cp", "proj:/workspace/README.md", "./"); err != nil {
		t.Fatalf("the documented example still fails: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Errorf("`cp foo:/workspace/README.md ./` did not produce ./README.md: %v", err)
	}
}
