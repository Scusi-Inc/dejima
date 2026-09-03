package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// forceChownFailure makes the tests exercise the MODE branch deterministically.
//
// Without this the tests are worthless on a runner that is itself uid 1000:
// chown-to-self succeeds, the function returns early, and a total no-op is
// indistinguishable from the fix. Three mutations passed against the first
// version of this file for exactly that reason.
func forceChownFailure(t *testing.T) {
	t.Helper()
	old := chownFn
	chownFn = func(string, int, int) error { return errors.New("not permitted") }
	t.Cleanup(func() { chownFn = old })
}

// Directories need +x, not just +r. The reported failure was the DIRECTORY:
//
//	ls: cannot open directory '/opt/host/gh-config': Permission denied
//
// before any file was reached.
func TestIslandReadablePerm(t *testing.T) {
	for _, tc := range []struct {
		in    os.FileMode
		isDir bool
		want  os.FileMode
	}{
		{0o700, true, 0o755},
		{0o600, false, 0o644},
		{0o755, true, 0o755},  // already fine, unchanged
		{0o644, false, 0o644}, // already fine, unchanged
	} {
		if got := islandReadablePerm(tc.in, tc.isDir); got != tc.want {
			t.Errorf("islandReadablePerm(%04o, dir=%v) = %04o, want %04o", tc.in, tc.isDir, got, tc.want)
		}
	}
	// Stated separately because it is the specific defect: a directory that is
	// readable but not enterable still fails.
	if islandReadablePerm(0o700, true)&0o001 == 0 {
		t.Error("directories are not made enterable; the island cannot open the mount")
	}
}

// A credential the island cannot read is the same as no credential — and it
// fails as an AUTH error, not a permission one, which is what made it cost
// hours. `dejima github connect` reported "islands can now clone and push as
// this identity", the identity was bound, default, and mounted, and every
// private clone failed with "Authentication failed".
func TestMaterializedCredentialBecomesReachable(t *testing.T) {
	forceChownFailure(t)

	dir := filepath.Join(t.TempDir(), "gh-config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(dir, "hosts.yml")
	if err := os.WriteFile(hosts, []byte("token: secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	makeIslandReadableTree(dir)

	di, _ := os.Stat(dir)
	if di.Mode().Perm()&0o005 != 0o005 {
		t.Errorf("directory left at %04o — the island cannot enter the mount", di.Mode().Perm())
	}
	fi, _ := os.Stat(hosts)
	if fi.Mode().Perm()&0o004 == 0 {
		t.Errorf("credential left at %04o — gh reports 'permission denied', git reports "+
			"'Authentication failed'", fi.Mode().Perm())
	}
}

// The walk must touch the directory and its entries and NOTHING ABOVE. The
// containing secrets tree stays 0700 so a token is never exposed to other users
// on the host; the container mounts the leaf and never traverses up.
func TestTreeNeverTouchesTheParent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "islands", "one")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	targets := islandReadableTargets(dir)
	if len(targets) != 2 {
		t.Fatalf("expected the dir and its one entry, got %v", targets)
	}
	for _, p := range targets {
		if p != dir && filepath.Dir(p) != dir {
			t.Errorf("walk reaches outside the mounted directory: %s", p)
		}
	}
	parent := filepath.Dir(dir)
	for _, p := range targets {
		if p == parent || p == root {
			t.Errorf("walk includes %s — widening a parent would expose a token to other "+
				"users on the host", p)
		}
	}
}

// Best-effort: on Docker Desktop neither step is needed and both may fail
// harmlessly. Breaking the platform that works to fix the one that does not
// would be a bad trade.
func TestMakeIslandReadableIsBestEffort(t *testing.T) {
	makeIslandReadable(filepath.Join(t.TempDir(), "missing"))
	makeIslandReadableTree(filepath.Join(t.TempDir(), "also-missing"))
}
