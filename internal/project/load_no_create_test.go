package project

import (
	"os"
	"path/filepath"
	"testing"
)

// Load and Exists are the two callers that reach for an island's config without
// intending to write. Both used to create ~/.dejima/projects/<name>/ as a side
// effect of asking, which is how four directories named after test islands ended
// up in a real operator's ~/.dejima.
//
// These assert the whole of HOME stays untouched rather than just the island
// dir: the creation happened three levels up the helper chain (Root →
// ProjectsDir → ProjectDir), so a narrower assertion would miss a partial
// regression.

func TestLoadMissingIslandCreatesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Load("ghost"); err == nil {
		t.Fatal("Load of a missing island should fail")
	}
	assertHomeUntouched(t, home, "Load")
}

func TestExistsCreatesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if Exists("ghost") {
		t.Fatal("Exists should be false for an island that was never created")
	}
	assertHomeUntouched(t, home, "Exists")
}

// Load still has to read a config that IS there — the fix must not have made the
// read path resolve somewhere else. Without this, deleting the ReadFile call
// entirely would satisfy the two tests above.
func TestLoadStillReadsAnExistingIsland(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := &Project{Name: "real"}
	if err := p.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("real")
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.Name != "real" {
		t.Errorf("loaded island name = %q, want %q", got.Name, "real")
	}
}

func assertHomeUntouched(t *testing.T, home, what string) {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	if len(entries) == 0 {
		return
	}
	var found []string
	for _, e := range entries {
		found = append(found, filepath.Join(home, e.Name()))
	}
	t.Errorf("%s created %v — a read must not bring state into being; "+
		"this is what let detached goroutines write into a $HOME that had already moved on", what, found)
}
