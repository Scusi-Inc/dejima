package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCheckout makes dir look like a dejima checkout to FindCheckout/Stat.
func writeCheckout(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/aoos/dejima\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The regression that made a daemon un-updatable: `service install` writes the
// meta whole, so a run from outside the checkout used to resolve nothing and
// overwrite a good SourceDir with "". Re-running install from $HOME must not
// destroy what a previous run correctly recorded.
func TestResolveSourceDirKeepsPreviousRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	checkout := filepath.Join(home, "coding", "dejima")
	writeCheckout(t, checkout)

	if err := SaveInstallMeta(InstallMeta{SourceDir: checkout, System: true}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	// Run from somewhere with no checkout above it — the case that erased it.
	outside := t.TempDir()
	t.Chdir(outside)

	if got := ResolveSourceDir(); got != checkout {
		t.Errorf("ResolveSourceDir() = %q, want the previously recorded %q", got, checkout)
	}
}

// Run from inside a checkout, cwd wins — including over a stale record.
func TestResolveSourceDirPrefersCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stale := filepath.Join(home, "old-tree")
	writeCheckout(t, stale)
	if err := SaveInstallMeta(InstallMeta{SourceDir: stale}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	current := filepath.Join(home, "current-tree")
	writeCheckout(t, current)
	t.Chdir(current)

	if got := ResolveSourceDir(); got != current {
		t.Errorf("ResolveSourceDir() = %q, want cwd's checkout %q", got, current)
	}
}

// install.sh clones to ~/.dejima-src, so a scripted install has a checkout even
// when nobody ever cd'd into it.
func TestResolveSourceDirFallsBackToInstallScriptClone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	scripted := filepath.Join(home, ".dejima-src")
	writeCheckout(t, scripted)
	t.Chdir(t.TempDir())

	if got := ResolveSourceDir(); got != scripted {
		t.Errorf("ResolveSourceDir() = %q, want %q", got, scripted)
	}
}

// Nothing anywhere: report "" honestly so the caller can warn, rather than
// inventing a path that would fail later inside a self-update.
func TestResolveSourceDirEmptyWhenNoneFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if got := ResolveSourceDir(); got != "" {
		t.Errorf("ResolveSourceDir() = %q, want empty", got)
	}
}
