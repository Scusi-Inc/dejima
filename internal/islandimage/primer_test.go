package islandimage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritePrimerIdempotent exercises the SHIPPED image/write-primer.sh (from the
// embedded build context) against real files: create, replace-in-place
// (self-refreshing, never duplicated), non-clobbering of surrounding content, and
// append to a pre-existing file — plus island-name substitution and the Port
// accuracy note. This is the idempotent-block test a3 asked for, run in CI.
func TestWritePrimerIdempotent(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir, cleanup, err := Materialize()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	script := filepath.Join(dir, "image", "write-primer.sh")
	template := filepath.Join(dir, "image", "island-primer.md")
	for _, p := range []string{script, template} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s in the materialized image context: %v", filepath.Base(p), err)
		}
	}

	run := func(target string) {
		t.Helper()
		cmd := exec.Command("bash", script, target)
		cmd.Env = append(os.Environ(), "PRIMER_TEMPLATE="+template, "DEJIMA_PROJECT_NAME=wildfire")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("write-primer.sh %s: %v\n%s", target, err, out)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	blocks := func(s string) int { return strings.Count(s, "BEGIN dejima island primer") }

	work := t.TempDir()
	target := filepath.Join(work, "CLAUDE.md")

	// 1. create — one block, island substituted, Port note present.
	run(target)
	got := read(target)
	if blocks(got) != 1 {
		t.Fatalf("create: want 1 block, got %d:\n%s", blocks(got), got)
	}
	if !strings.Contains(got, "`wildfire`") {
		t.Errorf("create: island name not substituted:\n%s", got)
	}
	if !strings.Contains(got, "Port") {
		t.Errorf("create: primer missing the Port accuracy note")
	}

	// 2. replace — still exactly one block (idempotent + self-refreshing).
	run(target)
	if n := blocks(read(target)); n != 1 {
		t.Errorf("replace: want 1 block, got %d", n)
	}

	// 3. non-clobbering — content outside the block survives a refresh.
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\nMY OWN GLOBAL NOTE\n")
	_ = f.Close()
	run(target)
	after := read(target)
	if !strings.Contains(after, "MY OWN GLOBAL NOTE") {
		t.Error("non-clobber: the user's own note was lost on refresh")
	}
	if n := blocks(after); n != 1 {
		t.Errorf("non-clobber: want 1 block, got %d", n)
	}

	// 4. append — a pre-existing file with no block keeps its content + gains one.
	other := filepath.Join(work, "AGENTS.md")
	if err := os.WriteFile(other, []byte("pre-existing project note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(other)
	got = read(other)
	if !strings.Contains(got, "pre-existing project note") || blocks(got) != 1 {
		t.Errorf("append: want existing content + 1 block, got:\n%s", got)
	}
}
