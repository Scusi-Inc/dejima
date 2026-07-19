package githubid

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestUpdateIsSerializedAndAtomic runs many concurrent Update calls, each adding
// a distinct identity, and asserts none are lost — i.e. the read-modify-write is
// serialized and the atomic write never corrupts the store.
func TestUpdateIsSerializedAndAtomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.dejima

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Update(func(s *Store) error {
				s.Put(Identity{Name: fmt.Sprintf("id%02d", i), Login: "u", Token: "t"})
				return nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Idents) != n {
		t.Fatalf("lost updates under concurrency: got %d identities, want %d", len(s.Identities), n)
	}
}

func TestPutSetsFirstAsDefault(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "personal", Login: "austin", Token: "t1"})
	if s.Default != "personal" {
		t.Fatalf("first Put should set default; got %q", s.Default)
	}
	s.Put(Identity{Name: "work", Login: "alockwood", Host: "github.example.com", Token: "t2"})
	if s.Default != "personal" {
		t.Errorf("later Put must not steal default; got %q", s.Default)
	}
	// Host defaults to github.com when unset.
	if id, _ := s.Resolve("personal"); id.Host != DefaultHost {
		t.Errorf("missing host should default to %q, got %q", DefaultHost, id.Host)
	}
}

func TestResolveNameAndDefault(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "personal", Login: "austin", Token: "t1"})
	s.Put(Identity{Name: "work", Login: "alockwood", Token: "t2"})
	_ = s.SetDefault("work")

	if id, ok := s.Resolve("personal"); !ok || id.Login != "austin" {
		t.Errorf("Resolve(personal) = %+v, %v", id, ok)
	}
	if id, ok := s.Resolve(""); !ok || id.Name != "work" {
		t.Errorf("Resolve(empty) should pick the default work; got %+v, %v", id, ok)
	}
	if _, ok := s.Resolve("nope"); ok {
		t.Error("Resolve(unknown) should be !ok")
	}
	empty := &Store{}
	if _, ok := empty.Resolve(""); ok {
		t.Error("Resolve(empty) on an empty store should be !ok")
	}
}

func TestListHasNoTokensAndSorts(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "work", Login: "alockwood", Token: "secret"})
	s.Put(Identity{Name: "personal", Login: "austin", Token: "secret"})
	list := s.List()
	if len(list) != 2 || list[0].Name != "personal" || list[1].Name != "work" {
		t.Fatalf("List not sorted by name: %+v", list)
	}
	// work was added first, so it's the default — independent of sort order.
	if list[0].Default || !list[1].Default {
		t.Errorf("default marking wrong (work added first): %+v", list)
	}
}

func TestRemoveReassignsDefault(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "personal", Login: "austin", Token: "t1"}) // default
	s.Put(Identity{Name: "work", Login: "alockwood", Token: "t2"})
	if !s.Remove("personal") {
		t.Fatal("Remove(personal) returned false")
	}
	if s.Default != "work" {
		t.Errorf("default should fall to work after removing personal; got %q", s.Default)
	}
	if s.Remove("personal") {
		t.Error("Remove of a missing identity should return false")
	}
	s.Remove("work")
	if s.Default != "" {
		t.Errorf("default should clear when no identities remain; got %q", s.Default)
	}
}

func TestHostsYAML(t *testing.T) {
	got := HostsYAML(Identity{Login: "austin", Token: "ghp_abc", Host: ""})
	for _, want := range []string{"github.com:", "oauth_token: ghp_abc", "user: austin", "git_protocol: https"} {
		if !strings.Contains(got, want) {
			t.Errorf("HostsYAML missing %q in:\n%s", want, got)
		}
	}
	// Must emit the modern multi-account schema (a per-user `users:` map), not
	// just the legacy top-level form. Without it gh migrates on first use and
	// writes to the read-only mount, which fails. See TestSetupGitOnReadOnlyDir.
	for _, want := range []string{"    users:\n", "        austin:\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("HostsYAML missing modern users block %q in:\n%s", want, got)
		}
	}
	// Enterprise host leads the document.
	ent := HostsYAML(Identity{Login: "alockwood", Token: "t", Host: "github.example.com"})
	if !strings.HasPrefix(ent, "github.example.com:\n") {
		t.Errorf("enterprise host should lead the yaml; got:\n%s", ent)
	}
}

// TestSetupGitOnReadOnlyDir is the regression test for the GitHub-identity
// crash-loop: the daemon mounts the materialized gh config read-only, so the
// config must already be in gh's migrated schema or `gh auth setup-git` tries to
// write back, fails, and the island ends up with no git credential helper.
//
// It writes HostsYAML+ConfigYAML into a dir, makes it read-only, and runs the
// real `gh auth setup-git` against it — asserting success and that the credential
// helper lands. Skipped when gh isn't installed (e.g. minimal CI).
func TestSetupGitOnReadOnlyDir(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh not installed; skipping read-only setup-git regression test")
	}

	cfgDir := t.TempDir()
	id := Identity{Login: "aoos", Token: "gho_dummyDummyDummyDummyDummyDummy0000", Host: ""}
	mustWrite(t, filepath.Join(cfgDir, "hosts.yml"), HostsYAML(id))
	mustWrite(t, filepath.Join(cfgDir, "config.yml"), ConfigYAML())

	// Mimic the daemon's read-only bind mount: no writes permitted in the dir.
	// Restore perms in cleanup so t.TempDir can remove the tree.
	for _, p := range []string{
		filepath.Join(cfgDir, "hosts.yml"),
		filepath.Join(cfgDir, "config.yml"),
		cfgDir,
	} {
		if err := os.Chmod(p, 0o500); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(cfgDir, 0o700)
		_ = os.Chmod(filepath.Join(cfgDir, "hosts.yml"), 0o600)
		_ = os.Chmod(filepath.Join(cfgDir, "config.yml"), 0o600)
	})

	// Isolate gh/git writes to a throwaway HOME so the dev box's global
	// gitconfig is never touched; the credential helper lands in GIT_CONFIG_GLOBAL.
	home := t.TempDir()
	gitConfig := filepath.Join(home, ".gitconfig")
	env := append(os.Environ(),
		"GH_CONFIG_DIR="+cfgDir,
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+gitConfig,
	)

	cmd := exec.Command(gh, "auth", "setup-git")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gh auth setup-git failed on read-only config dir: %v\n%s", err, out)
	}

	helpers, err := os.ReadFile(gitConfig)
	if err != nil {
		t.Fatalf("read git config: %v", err)
	}
	if !strings.Contains(string(helpers), "gh auth git-credential") {
		t.Errorf("setup-git did not install the gh credential helper; gitconfig:\n%s", helpers)
	}
}

func TestGitAuthor(t *testing.T) {
	// With a numeric id: the canonical ID-prefixed noreply email.
	name, email := GitAuthor(Identity{Login: "aoos", ID: 583231, Host: ""})
	if name != "aoos" {
		t.Errorf("name = %q, want aoos", name)
	}
	if email != "583231+aoos@users.noreply.github.com" {
		t.Errorf("email = %q, want 583231+aoos@users.noreply.github.com", email)
	}

	// Without a numeric id (identity stored before id capture): the older
	// login-only noreply form, still account-linked.
	_, email = GitAuthor(Identity{Login: "aoos"})
	if email != "aoos@users.noreply.github.com" {
		t.Errorf("fallback email = %q, want aoos@users.noreply.github.com", email)
	}

	// Enterprise host derives its own noreply domain.
	_, email = GitAuthor(Identity{Login: "alockwood", ID: 7, Host: "github.example.com"})
	if email != "7+alockwood@users.noreply.github.example.com" {
		t.Errorf("enterprise email = %q, want 7+alockwood@users.noreply.github.example.com", email)
	}
}

func TestGitConfig(t *testing.T) {
	got := GitConfig(Identity{Login: "aoos", ID: 583231})
	for _, want := range []string{"[user]", "name = aoos", "email = 583231+aoos@users.noreply.github.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("GitConfig missing %q in:\n%s", want, got)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
