package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// The read accessors must not bring state into existence. That is a correctness
// property in its own right — asking whether an island exists should not make a
// directory for it — and it is what stops detached goroutines from writing into
// a $HOME that has moved on since they were started.

func TestProjectConfigPathReadCreatesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ProjectConfigPathRead("ghost")
	if err != nil {
		t.Fatalf("ProjectConfigPathRead: %v", err)
	}
	if want := filepath.Join(home, ".dejima", "projects", "ghost", "config.toml"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// Not just the island dir — nothing at all, including ~/.dejima itself. A
	// lookup on a fresh machine should leave the disk as it found it.
	if entries, err := os.ReadDir(home); err != nil {
		t.Fatalf("read home: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("a read created %d entries under HOME: %v", len(entries), names(entries))
	}
}

// The positive control. If ProjectConfigPath ever stops creating, the test above
// passes for a reason that has nothing to do with the read path being read-only
// — and the assertion silently stops meaning anything.
func TestProjectConfigPathStillCreatesForWriters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := ProjectConfigPath("ghost"); err != nil {
		t.Fatalf("ProjectConfigPath: %v", err)
	}
	dir := filepath.Join(home, ".dejima", "projects", "ghost")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the writer path must create the island dir (Save writes into it): %v", err)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
