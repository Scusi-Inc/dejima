package api

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/secrets"
)

// inodeOf is the whole point of this file. The bug was invisible to every
// content-level assertion: the daemon wrote the right bytes to the right path
// every time, and the island still read stale values, because a file bind mount
// resolves to an INODE and the writer replaces the inode by rename.
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return uint64(st.Ino)
}

// What `dejima secret set` and `secret rm` used to do on a running island:
// report success and change nothing the island could see.
//
// The mount is fixed at container-create time. Mounting the FILE bound the inode
// that path resolved to then, and every rewrite since is a CreateTemp+Rename —
// a new inode at the same path — so the container kept reading the original for
// its whole life. Mounting the DIRECTORY makes the container resolve
// secrets.env per access, so the rename lands.
//
// Asserted as inode identity rather than by starting a container: this is the
// property Docker's bind actually depends on, and it needs no daemon.
func TestSecretsMountSurvivesRewrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secrets.BackendEnvVar, "file")
	store, err := secrets.OpenIsland()
	if err != nil {
		t.Fatal(err)
	}

	file, err := materializeIslandSecrets(store, "wildfire")
	if err != nil {
		t.Fatal(err)
	}
	mountDir, err := islandSecretsMountDir("wildfire")
	if err != nil {
		t.Fatal(err)
	}

	// The thing bind-mounted must be the DIRECTORY holding the file.
	if got := filepath.Dir(file); got != mountDir {
		t.Fatalf("secrets file is not inside the mounted directory:\n file:  %s\n mount: %s", got, mountDir)
	}
	if fi, err := os.Stat(mountDir); err != nil || !fi.IsDir() {
		t.Fatalf("the mounted path must be a directory: %v", err)
	}

	// The production accessor — what actually becomes the BindMount HostPath —
	// must hand back the directory, not the file. Asserted here because every
	// check above would still pass if this one function went back to returning
	// the file path.
	got, err := islandSecretsMount(&project.Project{Name: "wildfire"})
	if err != nil {
		t.Fatal(err)
	}
	if got != mountDir {
		t.Fatalf("islandSecretsMount returned %q, want the mounted directory %q "+
			"— returning the FILE is the bug: a file bind binds the inode", got, mountDir)
	}

	dirInoBefore := inodeOf(t, mountDir)
	fileInoBefore := inodeOf(t, file)

	if _, err := store.Set("wildfire", "EXPO_TOKEN", "tok-abc", "aoos"); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeIslandSecrets(store, "wildfire"); err != nil {
		t.Fatal(err)
	}

	// The file's inode is EXPECTED to change — the atomic rename is deliberate
	// and worth keeping. If this ever stops being true the mount bug could come
	// back unnoticed via a different route, so assert it rather than assume it.
	if inodeOf(t, file) == fileInoBefore {
		t.Error("secrets file kept its inode across a rewrite — this test no longer " +
			"exercises the condition that made a file mount go stale")
	}

	// The mounted directory's inode must NOT change. This is what a container
	// created earlier is still holding.
	if got := inodeOf(t, mountDir); got != dirInoBefore {
		t.Errorf("the mounted directory was replaced (inode %d -> %d); a container "+
			"created before the rewrite is now reading a detached directory, which is "+
			"the same staleness the file mount had", dirInoBefore, got)
	}

	// And the new value must be readable through the path a container resolves.
	b, err := os.ReadFile(filepath.Join(mountDir, secretsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "EXPO_TOKEN=tok-abc") {
		t.Errorf("the rewritten value is not visible through the mounted directory:\n%s", b)
	}
}

// The island's whole secrets dir also holds meta.json. Only the mount
// subdirectory is exposed — not to plug a leak (meta.json carries fingerprints,
// never values) but so a file added to that dir later cannot silently become
// island-visible.
func TestOnlyTheMountSubdirIsExposed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secrets.BackendEnvVar, "file")
	store, err := secrets.OpenIsland()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("wildfire", "EXPO_TOKEN", "tok-abc", "aoos"); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeIslandSecrets(store, "wildfire"); err != nil {
		t.Fatal(err)
	}
	mountDir, err := islandSecretsMountDir("wildfire")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(mountDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != secretsFileName {
			t.Errorf("%q is inside the island's bind mount but is not the secrets file; "+
				"anything in this directory is readable by every agent in the island", e.Name())
		}
	}
}
