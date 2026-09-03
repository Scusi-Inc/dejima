package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/localmodel"
)

func statusWithRAM(ramGiB int, pulled ...string) *localmodel.Status {
	st := &localmodel.Status{Backend: "ollama", Installed: true, Running: true, HostRAMGiB: ramGiB}
	for _, ref := range pulled {
		st.Models = append(st.Models, localmodel.InstalledModel{Ref: ref})
	}
	if ramGiB > 0 {
		st.Recommend = localmodel.RecommendFor(ramGiB)
	}
	return st
}

// The whole point of the picker: every curated model is choosable, not just the
// one the recommendation happens to name. An operator on a 16 GiB laptop who
// wants the 3B for autocomplete had to go and read `dejima local ls` to find out
// what to type.
func TestThePickerOffersTheWholeCatalog(t *testing.T) {
	rows := localModelRows(statusWithRAM(64))
	if len(rows) != len(localmodel.Catalog) {
		t.Fatalf("picker shows %d rows for a %d-model catalog", len(rows), len(localmodel.Catalog))
	}
	for i, r := range rows {
		if r.model.Alias != localmodel.Catalog[i].Alias {
			t.Errorf("row %d is %q, catalog order says %q — small-to-large is the scan order",
				i, r.model.Alias, localmodel.Catalog[i].Alias)
		}
	}
}

// A model that does not fit stays VISIBLE and is marked, rather than being
// hidden. A list that silently drops options cannot be told apart from a list
// that is complete — and the operator may know something the RAM number does
// not (an upgrade tomorrow, the wrong host on screen).
func TestModelsThatDoNotFitAreMarkedNotHidden(t *testing.T) {
	rows := localModelRows(statusWithRAM(16))

	var big, small *localModelRow
	for i := range rows {
		switch {
		case rows[i].model.MinRAMGiB > 16 && big == nil:
			big = &rows[i]
		case rows[i].model.MinRAMGiB <= 16 && small == nil:
			small = &rows[i]
		}
	}
	if big == nil || small == nil {
		t.Fatal("a 16 GiB host should have both fitting and non-fitting models in the catalog")
	}

	if !big.fitsKnown || big.fits {
		t.Errorf("%s needs %d GiB and the host has 16 — it must be marked as not fitting",
			big.model.Alias, big.model.MinRAMGiB)
	}
	label := localModelRowLabel(*big)
	if !strings.Contains(label, "needs") {
		t.Errorf("a non-fitting row must say what it needs, got %q", label)
	}
	// The NUMBER, not a verdict: "needs 48 GiB" is actionable, "too big" is not.
	if !strings.Contains(label, "GiB") {
		t.Errorf("a non-fitting row must name the requirement, got %q", label)
	}
	if !small.fits {
		t.Errorf("%s needs %d GiB and the host has 16 — it fits", small.model.Alias, small.model.MinRAMGiB)
	}
	// ...and it is still offered, because the choice stays the operator's.
	var offered bool
	for _, a := range localModelActions(statusWithRAM(16)) {
		if a.verb == "pull "+big.model.Alias {
			offered = true
		}
	}
	if !offered {
		t.Errorf("%s was removed from the picker for not fitting — a picker that refuses "+
			"is one people work around", big.model.Alias)
	}
}

// UNKNOWN HOST RAM IS NOT A PASS. HostRAMGiB is 0 when the daemon could not
// determine it, and telling someone a model fits when nothing measured is the
// reassuring-direction failure this codebase keeps producing.
func TestUnknownHostRAMNeverImpliesAFit(t *testing.T) {
	rows := localModelRows(&localmodel.Status{Backend: "ollama", Installed: true}) // HostRAMGiB 0
	if len(rows) == 0 {
		t.Fatal("no rows at all")
	}
	for _, r := range rows {
		if r.fitsKnown {
			t.Errorf("%s claims a known fit with no host RAM figure", r.model.Alias)
		}
		label := localModelRowLabel(r)
		if !strings.Contains(label, "host RAM unknown") {
			t.Errorf("%s does not say the fit is unknown: %q", r.model.Alias, label)
		}
		if strings.Contains(label, "recommended") && !strings.Contains(label, "unknown") {
			t.Errorf("%s reads as recommended without a measurement behind it: %q", r.model.Alias, label)
		}
	}
}

// A pulled model is marked and is NOT offered again — a re-pull is a
// multi-gigabyte no-op, and an action that appears to do something while doing
// nothing is the shape this file exists to avoid.
func TestAPulledModelIsMarkedAndNotOfferedAgain(t *testing.T) {
	target := localmodel.Catalog[1]
	st := statusWithRAM(64, target.Ref)

	var row *localModelRow
	rows := localModelRows(st)
	for i := range rows {
		if rows[i].model.Alias == target.Alias {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("%s missing from the picker", target.Alias)
	}
	if !row.pulled {
		t.Errorf("%s is on the host (by ref %q) and the row does not know it", target.Alias, target.Ref)
	}
	if !strings.Contains(localModelRowLabel(*row), "pulled") {
		t.Errorf("a pulled row must say so: %q", localModelRowLabel(*row))
	}
	for _, a := range localModelActions(st) {
		if a.verb == "pull "+target.Alias {
			t.Errorf("%s is already pulled and was offered again", target.Alias)
		}
	}
}

// The recommendation is a fact about THIS host and belongs beside the thing it
// recommends, not in a sentence somewhere else on the page.
func TestTheRecommendationIsMarkedOnItsOwnRow(t *testing.T) {
	st := statusWithRAM(64)
	if st.Recommend.Top == nil {
		t.Fatal("a 64 GiB host should get a recommendation")
	}
	found := false
	for _, r := range localModelRows(st) {
		if r.model.Alias != st.Recommend.Top.Alias {
			if r.recommend {
				t.Errorf("%s is marked recommended but is not the recommendation", r.model.Alias)
			}
			continue
		}
		found = true
		if !r.recommend {
			t.Errorf("%s IS the recommendation and its row does not say so", r.model.Alias)
		}
		if !strings.Contains(localModelRowLabel(r), "recommended") {
			t.Errorf("the recommended row does not say so: %q", localModelRowLabel(r))
		}
	}
	if !found {
		t.Error("the recommended model has no row")
	}
}

// THE QUESTION THE OPERATOR ASKED: what has to be restarted for islands to see
// this? The answer is narrower than it looks, and every wider guess costs them
// something real — a recreate, or a daemon bounce that closes every terminal.
func TestTheRestartNoteNamesTheAgentAndRulesOutTheRest(t *testing.T) {
	note := localModelsAppliedNote(statusWithRAM(64, localmodel.Catalog[0].Ref))
	if note == "" {
		t.Fatal("a pulled model should explain what to restart")
	}
	low := strings.ToLower(note)
	// THE INSTRUCTION AS A PHRASE, not its words scattered anywhere in the note.
	// This asserted Contains("restart") && Contains("agent"), and a mutation that
	// DELETED the instruction survived it: "to use it from an agent:" supplies
	// "agent" and "no daemon restart" supplies "restart", so both halves passed
	// with nothing left telling the operator what to do. Two true substrings
	// composing into a false conclusion — the same shape as everything else this
	// week, inside the guard written against it.
	if !strings.Contains(low, "restart the agent") {
		t.Errorf("the note must tell the operator to restart the agent, in those words, got %q", note)
	}
	// ...and name the affordance, so it is an instruction rather than a fact.
	if !strings.Contains(note, "[s]") || !strings.Contains(note, "Restart") {
		t.Errorf("the note must point at the key that does it, got %q", note)
	}
	// The two costly wrong guesses it rules out. Both are now true again as of
	// 8b850d1 (#396), which made the /opt/host/llm mount unconditional and
	// resolved the provider key file per agent at launch.
	if !strings.Contains(low, "no daemon restart") || !strings.Contains(low, "no island recreate") {
		t.Errorf("the note must rule out the daemon restart and the recreate, got %q", note)
	}
	// AND IT MUST NOT HEDGE ANY MORE. For about an hour this note carried a second
	// bullet sending some operators to `dejima upgrade`, because the mount used to
	// be conditional at container create. 8b850d1 removed that class of island
	// entirely. A hedge kept for a case that no longer exists is its own small
	// lie, and the kind that survives for years: nobody can disprove it by trying,
	// they just do the extra step and it works.
	if strings.Contains(low, "upgrade") || strings.Contains(low, "cannot tell") {
		t.Errorf("the note still hedges about a recreate no island needs since 8b850d1: %q", note)
	}

	// Nothing to say before the backend exists.
	if got := localModelsAppliedNote(&localmodel.Status{Backend: "ollama"}); got != "" {
		t.Errorf("an uninstalled backend should not talk about restarts, got %q", got)
	}
	// AND NOTHING BEFORE A MODEL EXISTS. "islands can reach this now" with the
	// backend installed and nothing pulled is a capability claim about an
	// endpoint that would answer every request with "model not found".
	if got := localModelsAppliedNote(statusWithRAM(64)); got != "" {
		t.Errorf("an installed backend with no models claims islands can reach it: %q", got)
	}
	if got := localModelsAppliedNote(nil); got != "" {
		t.Errorf("no status should say nothing, got %q", got)
	}
}
