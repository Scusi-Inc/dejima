package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime/runtimetest"
)

// --- create-time validation ------------------------------------------------

// from_dir is its own workspace source. Combining it with another is a
// contradiction, and guessing which one was meant is how you end up cloning into
// an island someone believed held their folder.
func TestCreate_FromDirIsExclusive(t *testing.T) {
	for _, body := range []string{
		`{"name":"x","from_dir":"/tmp/f","repo":"https://github.com/a/b"}`,
		`{"name":"x","from_dir":"/tmp/f","seed_path":"/tmp/seed"}`,
		`{"name":"x","from_dir":"/tmp/f","no_repo":true}`,
	} {
		code, resp := postCreate(t, &Server{}, body)
		if code != http.StatusBadRequest {
			t.Errorf("expected rejection for %s, got %d: %s", body, code, resp)
		}
		if !strings.Contains(resp, "own workspace source") {
			t.Errorf("error should say the sources are exclusive, got: %s", resp)
		}
	}
}

// A folder's basename is a bad island name: "src", "notes" and "tmp" recur
// everywhere, so deriving one produces unpredictable and colliding names.
func TestCreate_FromDirRequiresName(t *testing.T) {
	code, resp := postCreate(t, &Server{}, `{"from_dir":"/tmp/f"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected rejection, got %d: %s", code, resp)
	}
	if !strings.Contains(resp, "name is required") {
		t.Errorf("error should say a name is required, got: %s", resp)
	}
}

// The two modifiers are meaningless alone. Accepting them silently would let
// someone believe they had asked for something they had not.
func TestCreate_FromDirModifiersNeedFromDir(t *testing.T) {
	for _, body := range []string{
		`{"name":"x","repo":"https://github.com/a/b","git_init":true}`,
		`{"name":"x","repo":"https://github.com/a/b","keep_scope":true}`,
	} {
		code, resp := postCreate(t, &Server{}, body)
		if code != http.StatusBadRequest {
			t.Errorf("expected rejection for %s, got %d: %s", body, code, resp)
		}
		if !strings.Contains(resp, "only applies with from_dir") {
			t.Errorf("error should name the dependency, got: %s", resp)
		}
	}
}

// The "you must give a source" error has to mention from_dir, or the feature is
// invisible at exactly the moment someone needs it.
func TestCreate_MissingSourceMentionsFromDir(t *testing.T) {
	code, resp := postCreate(t, &Server{}, `{"name":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected rejection, got %d: %s", code, resp)
	}
	for _, want := range []string{"from_dir", "no_repo"} {
		if !strings.Contains(resp, want) {
			t.Errorf("the error should offer %q as an alternative, got: %s", want, resp)
		}
	}
}

// --- seeding ---------------------------------------------------------------

// The happy path, end to end through the fake runtime: every regular file
// crosses, symlinks are skipped, and the scope is dropped afterwards.
func TestSeedWorkspace_CopiesAndDropsTheScope(t *testing.T) {
	scope, _ := treeFixture(t) // a.txt, sub/b.txt, three symlinks
	s, p := seedServer(t)

	if err := s.seedWorkspaceFromDir(context.Background(), p, folderSeed{Dir: scope}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n := copyCount(s); n != 2 {
		t.Errorf("copied %d files, want the 2 regular ones (symlinks are skipped)", n)
	}
	// The grant was needed to copy, not to keep. A scope left behind is standing
	// host-file access nobody asked for.
	if len(p.Ports) != 0 {
		t.Errorf("scope survived the seed: %+v — that is host access granted as a "+
			"side effect of a create flag", p.Ports)
	}
}

// --keep-scope is the opt-in, and it must actually keep it.
func TestSeedWorkspace_KeepScopeRetainsIt(t *testing.T) {
	scope, _ := treeFixture(t)
	s, p := seedServer(t)

	if err := s.seedWorkspaceFromDir(context.Background(), p, folderSeed{Dir: scope, KeepScope: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(p.Ports) != 1 {
		t.Fatalf("scope count = %d, want the one that was asked for", len(p.Ports))
	}
	if p.Ports[0].Mode != "ro" {
		t.Errorf("seed scope mode = %q, want ro — seeding only ever reads", p.Ports[0].Mode)
	}
}

// Over-cap folders are refused BEFORE a scope exists. Refusing after granting
// would leave the operator holding host access for a copy that never happened.
func TestSeedWorkspace_OverCapRefusesBeforeGranting(t *testing.T) {
	base := t.TempDir()
	for i := 0; i < defaultIntakeMaxFiles+1; i++ {
		mustWrite(t, filepath.Join(base, "f"+strconv.Itoa(i)+".txt"), "x")
	}
	s, p := seedServer(t)

	err := s.seedWorkspaceFromDir(context.Background(), p, folderSeed{Dir: base})
	if err == nil {
		t.Fatal("an over-cap folder was accepted")
	}
	if len(p.Ports) != 0 {
		t.Errorf("a scope was granted for a refused seed: %+v", p.Ports)
	}
	if n := copyCount(s); n != 0 {
		t.Errorf("%d files copied despite the refusal", n)
	}
	// The refusal must name the way forward, or the operator is simply stuck.
	if !strings.Contains(err.Error(), "port intake") {
		t.Errorf("refusal should name the two-step path, got: %v", err)
	}
}

// A folder of only symlinks looks like an empty one once they are skipped, so
// the refusal has to say which happened.
func TestSeedWorkspace_EmptyFolderRefusedWithReason(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	mustMkdir(t, src)
	mustWrite(t, filepath.Join(base, "target"), "x")
	mustSymlink(t, filepath.Join(base, "target"), filepath.Join(src, "link"))

	s, p := seedServer(t)

	err := s.seedWorkspaceFromDir(context.Background(), p, folderSeed{Dir: src})
	if err == nil {
		t.Fatal("a folder with no regular files was accepted")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal should say WHY it found nothing, got: %v", err)
	}
}

// A file is not a folder. Saying so, and naming the single-file command, beats a
// confusing walk error.
func TestSeedWorkspace_RejectsAFile(t *testing.T) {
	base := t.TempDir()
	f := filepath.Join(base, "one.txt")
	mustWrite(t, f, "x")
	s, p := seedServer(t)

	err := s.seedWorkspaceFromDir(context.Background(), p, folderSeed{Dir: f})
	if err == nil || !strings.Contains(err.Error(), "port intake") {
		t.Errorf("a single file should be refused with the right command named, got: %v", err)
	}
}

// --- helpers ---------------------------------------------------------------

// seedServer builds a server whose island has NO Port scopes, so the scope
// assertions below count only what seeding itself granted. intakeServer
// pre-grants one, which would make "the seed left a scope behind" and "the
// fixture had one" indistinguishable — the exact ambiguity these tests exist to
// remove.
func seedServer(t *testing.T) (*Server, *project.Project) {
	t.Helper()
	s, _ := intakeServer(t, t.TempDir())
	p, err := project.Load("isl")
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	p.Ports = nil
	if err := p.Save(); err != nil {
		t.Fatalf("save project: %v", err)
	}
	return s, p
}

func copyCount(s *Server) int { return s.rt.(*runtimetest.Fake).CopyCount() }
