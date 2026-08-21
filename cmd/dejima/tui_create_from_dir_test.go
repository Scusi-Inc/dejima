package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func typeInto(m tuiModel, s string) tuiModel {
	for _, r := range s {
		m = feedCreator(m, string(r))
	}
	return m
}

// The folder source is the fourth answer to "what goes in /workspace?". Choosing
// it must skip every repo-source step — there is no URL, no origin, nothing to
// resolve — and land on the name prompt, because a folder's basename ("src",
// "notes", "tmp") makes an unpredictable and colliding island name.
func TestCreator_FromDirSkipsRepoStepsAndAsksForAName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := tuiModel{creator: &creatorModel{step: stepPick, repoCursor: pickRowFromDir}}
	m = feedCreator(m, "enter")
	if m.creator.step != stepFromDir {
		t.Fatalf("step = %v, want stepFromDir", m.creator.step)
	}
	m = typeInto(m, dir)
	m = feedCreator(m, "enter")

	c := m.creator
	if c.step != stepName {
		t.Fatalf("step = %v, want stepName", c.step)
	}
	if c.fromDir != dir {
		t.Errorf("fromDir = %q, want %q", c.fromDir, dir)
	}
	if c.nameInput != "" {
		t.Errorf("nameInput = %q, want empty — a folder basename is a poor island name", c.nameInput)
	}
	if c.resolution.Repo != "" {
		t.Errorf("a folder source resolved a repo: %q", c.resolution.Repo)
	}
	if !strings.Contains(c.resolution.Note, "Ledger") {
		t.Errorf("the note should say the copy is brokered and ledgered, got %q", c.resolution.Note)
	}
}

// A path that isn't a directory, or doesn't exist, is caught HERE — before a
// name and an agent have been chosen. Discovering it at create time would mean
// answering three more questions before being told the first was wrong.
func TestCreator_FromDirValidatesBeforeAdvancing(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "one.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, path, want string }{
		{"a file is not a folder", file, "not a folder"},
		{"a missing path", filepath.Join(base, "nope"), "can't read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tuiModel{creator: &creatorModel{step: stepFromDir}}
			m = typeInto(m, tc.path)
			m = feedCreator(m, "enter")
			if m.creator.step != stepFromDir {
				t.Fatalf("advanced past an invalid folder (step %v)", m.creator.step)
			}
			if !strings.Contains(m.creator.err, tc.want) {
				t.Errorf("err = %q, want it to mention %q", m.creator.err, tc.want)
			}
		})
	}
}

// An empty path must not advance — submitting nothing would otherwise carry an
// empty FromDir into the request, where the daemon rejects it after the operator
// has answered every remaining question.
func TestCreator_FromDirEmptyPathIsRefused(t *testing.T) {
	m := tuiModel{creator: &creatorModel{step: stepFromDir}}
	m = feedCreator(m, "enter")
	if m.creator.step != stepFromDir {
		t.Fatal("an empty folder path advanced")
	}
	if m.creator.err == "" {
		t.Error("refused silently — the user sees nothing happen and presses it again")
	}
}

// `git init` is off unless asked for, and the view has to state the cost at the
// moment of choosing. A repo with no remote makes the agent commit where nothing
// can be pushed, so work looks saved and is not.
func TestCreator_FromDirGitInitIsOptInAndWarns(t *testing.T) {
	m := tuiModel{creator: &creatorModel{step: stepFromDir}}
	if m.creator.fromDirGit {
		t.Error("git init defaults on")
	}
	var b strings.Builder
	m.creator.viewFromDir(&b)
	if strings.Contains(b.String(), "WARNING") {
		t.Error("the warning shows while git init is OFF — it would read as a warning about the default")
	}

	m = feedCreator(m, "tab")
	if !m.creator.fromDirGit {
		t.Fatal("tab did not enable git init")
	}
	b.Reset()
	m.creator.viewFromDir(&b)
	for _, want := range []string{"WARNING", "no remote"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the git-init view is missing %q; the cost must be stated where "+
				"the choice is made, got:\n%s", want, b.String())
		}
	}
}

// The field has to survive into the request, or every screen above it is theatre.
func TestCreator_FromDirReachesTheRequest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{creator: &creatorModel{step: stepPick, repoCursor: pickRowFromDir}}
	m = feedCreator(m, "enter")
	m = typeInto(m, dir)
	m = feedCreator(m, "tab") // git init on, so both fields are exercised
	m = feedCreator(m, "enter")
	m = typeInto(m, "notes")
	m = feedCreator(m, "enter")
	m.creator.agents = []api.AgentSpecRequest{{Type: "claude-code"}}

	req := m.creator.buildRequest()
	if req.FromDir != dir {
		t.Errorf("FromDir = %q, want %q — the daemon would create an island with no files", req.FromDir, dir)
	}
	if !req.GitInit {
		t.Error("GitInit did not reach the request")
	}
	if req.Name != "notes" {
		t.Errorf("Name = %q, want notes", req.Name)
	}
	if req.Repo != "" || req.NoRepo {
		t.Errorf("a folder source sent repo=%q no_repo=%v; the daemon refuses that combination", req.Repo, req.NoRepo)
	}
}
