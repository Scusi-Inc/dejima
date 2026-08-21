package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// The TUI creator always demanded a repo, so `--no-repo` (#348) was CLI-only and a
// repo-less island could not be made from the dashboard at all. These drive the real
// key handler through the wizard rather than asserting on source, because the bug
// this branch can reintroduce is a step-machine bug: arriving at a step with state
// some earlier step was supposed to have built.

// newPickCreator puts the creator at the repo picker with no local repos found —
// the state a fresh `n` lands in on a host with nothing scanned.
func newPickCreator() tuiModel {
	return tuiModel{creator: &creatorModel{step: stepPick}}
}

// Choosing "start empty" must skip every repo-source step and land on the name
// prompt with nothing resolved and nothing prefilled.
func TestCreator_NoRepoSkipsSourceStepsAndAsksForAName(t *testing.T) {
	m := newPickCreator()
	m.creator.repoCursor = pickRowNoRepo
	m = feedCreator(m, "enter")
	c := m.creator

	if !c.noRepo {
		t.Error("noRepo not set — the request would go out as an empty repo, which the daemon rejects")
	}
	if c.step != stepName {
		t.Errorf("step = %v, want stepName: a repo-less island has nothing to derive a name from, "+
			"so the name stops being optional", c.step)
	}
	if c.nameInput != "" {
		t.Errorf("nameInput = %q, want empty — uniqueName(\"\") yields %q via DeriveNameFromRepo's "+
			"fallback, and auto-generating a name is exactly what the CLI refuses to do",
			c.nameInput, "island")
	}
	if c.resolution.Repo != "" || c.resolution.SeedPath != "" {
		t.Errorf("resolution should be empty, got repo=%q seed=%q", c.resolution.Repo, c.resolution.SeedPath)
	}
	if !strings.Contains(c.resolution.Note, "no repo") {
		t.Errorf("Note = %q; it is rendered at every later step and is what keeps an empty "+
			"/workspace reading as deliberate rather than as a failed clone", c.resolution.Note)
	}
}

// The name requirement, enforced by the step we routed through.
func TestCreator_NoRepoWontAdvanceWithoutAName(t *testing.T) {
	m := newPickCreator()
	m.creator.repoCursor = pickRowNoRepo
	m = feedCreator(m, "enter", "enter") // pick "start empty", then submit an empty name

	if m.creator.step != stepName {
		t.Fatalf("step = %v, want stepName — an empty name must not advance", m.creator.step)
	}

	// A typed name does advance, and the picker must already be built: stepName's
	// enter goes straight to stepAgent, so anything it depends on has to have been
	// initialised back when "start empty" was chosen.
	m = feedCreator(m, "b", "r", "a", "i", "n", "enter")
	if m.creator.step != stepAgent {
		t.Fatalf("step = %v, want stepAgent after a valid name", m.creator.step)
	}
	if m.creator.nameInput != "brain" {
		t.Errorf("nameInput = %q, want brain", m.creator.nameInput)
	}
	if got := m.creator.picker.typ(); got == "" {
		t.Error("agent picker is uninitialised at stepAgent — the wizard would render an " +
			"empty chooser, or panic on the first keypress")
	}
}

// The field has to survive into the request. Everything above could be right and
// the island would still come out wrong if this one line were dropped.
func TestCreator_NoRepoReachesTheRequest(t *testing.T) {
	m := newPickCreator()
	m.creator.repoCursor = pickRowNoRepo
	m = feedCreator(m, "enter", "b", "r", "a", "i", "n", "enter")

	// buildRequest reads agents[0], so seed the roster the picker would have built.
	// Driving the picker itself is TestCreator_NoRepoWontAdvanceWithoutAName's job;
	// this test is about what crosses the wire.
	m.creator.agents = []api.AgentSpecRequest{{Type: "openclaw"}}

	req := m.creator.buildRequest()
	if !req.NoRepo {
		t.Error("NoRepo absent from the request — the daemon rejects an empty repo without it, " +
			"so the create fails with a message about a flag the operator never saw")
	}
	if req.Repo != "" || req.SeedPath != "" {
		t.Errorf("repo-less request carries a source: repo=%q seed=%q", req.Repo, req.SeedPath)
	}
	if req.Name != "brain" {
		t.Errorf("Name = %q, want brain", req.Name)
	}
}

// The default action must never be "create an empty island". This asserts the
// BEHAVIOUR, not an index: an earlier version of this test pinned
// pickRowGitHub == 0, which broke the moment "Start empty" was moved to the top
// of the list — a correct UI change failing a test that had over-specified. What
// must hold is that repoCursor's initial value does not select the repo-less row,
// however the rows are ordered.
func TestCreator_DefaultRowDoesNotCreateAnEmptyIsland(t *testing.T) {
	m := tuiModel{}
	mm, _ := m.openCreator()
	c := mm.(tuiModel).creator
	if c == nil {
		t.Fatal("creator did not open")
	}
	if c.repoCursor == pickRowNoRepo {
		t.Fatal("the picker opens on `start empty`; `n` then ⏎ would create an " +
			"empty island for anyone who presses Enter twice")
	}
	if c.repoCursor == pickRowFromDir {
		t.Error("the picker opens on the folder-source row, which then demands a path")
	}

	m2 := tuiModel{creator: &creatorModel{step: stepPick, repoCursor: c.repoCursor}}
	m2 = feedCreator(m2, "enter")
	if m2.creator.noRepo {
		t.Error("the default picker row took the repo-less branch")
	}
}

// With no local repos discovered, the cursor must still be able to reach the new
// row; clamping to the old last row would make it unselectable on exactly the hosts
// most likely to want it (a fresh box with nothing checked out).
func TestCreator_NoRepoReachableWhenNoLocalReposFound(t *testing.T) {
	m := newPickCreator()
	m.creator.repos = nil
	// Walk down past every action row; the last one must be reachable and the
	// cursor must clamp there rather than running off the end into a repo that
	// does not exist.
	for i := 0; i < 8; i++ {
		m = feedCreator(m, "down")
	}
	if m.creator.repoCursor != pickRowFromDir {
		t.Fatalf("repoCursor = %d, want %d — the last action row is unreachable or "+
			"the cursor ran past it when no local repos were found",
			m.creator.repoCursor, pickRowFromDir)
	}
}
