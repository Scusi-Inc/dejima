package reposrc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsURL(t *testing.T) {
	cases := map[string]bool{
		"https://github.com/o/r.git": true,
		"git@github.com:o/r.git":     true,
		"ssh://git@host/o/r":         true,
		"/Users/me/code/repo":        false,
		"./repo":                     false,
		"repo":                       false,
		"~/code/repo":                false,
	}
	for in, want := range cases {
		if got := IsURL(in); got != want {
			t.Errorf("IsURL(%q) = %v, want %v", in, got, want)
		}
	}
}

// writeRepo creates a fake repo dir with a .git/config carrying the given origin
// (or no origin when empty).
func writeRepo(t *testing.T, dir, origin string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\trepositoryformatversion = 0\n"
	if origin != "" {
		cfg += "[remote \"origin\"]\n\turl = " + origin + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOriginURL(t *testing.T) {
	dir := t.TempDir()
	writeRepo(t, dir, "git@github.com:o/r.git")
	got, err := OriginURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com:o/r.git" {
		t.Errorf("OriginURL = %q", got)
	}

	noRemote := t.TempDir()
	writeRepo(t, noRemote, "")
	if got, _ := OriginURL(noRemote); got != "" {
		t.Errorf("OriginURL(no remote) = %q, want empty", got)
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	writeRepo(t, filepath.Join(root, "alpha"), "git@github.com:o/alpha.git")
	writeRepo(t, filepath.Join(root, "nested", "beta"), "")
	// A plain dir with no .git should be ignored.
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}

	repos, err := Discover(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Repo{}
	for _, r := range repos {
		byName[r.Name] = r
	}
	if len(byName) != 2 {
		t.Fatalf("Discover found %d repos, want 2: %+v", len(byName), repos)
	}
	if byName["alpha"].Origin != "git@github.com:o/alpha.git" {
		t.Errorf("alpha origin = %q", byName["alpha"].Origin)
	}
	if _, ok := byName["beta"]; !ok {
		t.Errorf("expected nested repo 'beta' to be discovered")
	}
}

func TestResolve(t *testing.T) {
	// A URL always resolves to a direct remote clone, no seed.
	res, err := Resolve("git@github.com:o/r.git", true, false)
	if err != nil || res.SeedPath != "" || res.Repo != "git@github.com:o/r.git" {
		t.Fatalf("URL resolve = %+v, err %v", res, err)
	}

	// Local path with an origin, default: clone from origin, no seed.
	withRemote := t.TempDir()
	writeRepo(t, withRemote, "git@github.com:o/r.git")
	res, err = Resolve(withRemote, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repo != "git@github.com:o/r.git" || res.SeedPath != "" {
		t.Errorf("local+origin default = %+v, want remote clone", res)
	}

	// Same repo, forced local copy against a local daemon: seed mount set,
	// origin preserved as the upstream.
	res, err = Resolve(withRemote, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.SeedPath != withRemote || res.Repo != "git@github.com:o/r.git" {
		t.Errorf("forced local copy = %+v, want seed=%s", res, withRemote)
	}

	// Local-only repo against a local daemon: seed mount, no origin.
	localOnly := t.TempDir()
	writeRepo(t, localOnly, "")
	res, err = Resolve(localOnly, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.SeedPath != localOnly || res.Repo != "" {
		t.Errorf("local-only = %+v, want seed with empty origin", res)
	}

	// Local-only repo against a REMOTE daemon: cannot seed across machines.
	if _, err := Resolve(localOnly, false, false); err == nil {
		t.Errorf("local-only + remote daemon should error")
	}

	// Local path with origin against a remote daemon: falls back to remote clone.
	res, err = Resolve(withRemote, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repo != "git@github.com:o/r.git" || res.SeedPath != "" {
		t.Errorf("remote daemon + origin = %+v, want remote clone fallback", res)
	}

	// Not a git repo at all.
	if _, err := Resolve(t.TempDir(), true, false); err == nil {
		t.Errorf("non-repo path should error")
	}
}
