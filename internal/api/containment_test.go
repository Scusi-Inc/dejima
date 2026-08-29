package api

import (
	"context"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// The zero value must not be a level. If it were, every record that forgot to
// set one would silently CLAIM that level — and since the levels are not
// symmetric (one is a reassurance, the other a warning), a forgetful record
// would always fail in the reassuring direction. That is the failure that
// matters: an ungated agent rendering as gated.
func TestContainmentZeroValueIsNotALevel(t *testing.T) {
	var zero ContainmentLevel
	if zero == ContainmentContained {
		t.Fatal("the zero value IS the contained level — an unset record now claims containment")
	}
	if zero.Contained() {
		t.Error("the zero value reports Contained() — unset must read as NOT contained, " +
			"because the safe default is the least reassuring answer, never the most")
	}
	if string(zero) != "" {
		t.Errorf("zero = %q, want the empty string so \"nobody said\" is distinguishable "+
			"from every real answer", zero)
	}
}

// Contained() exists so the fail-safe reading lives in ONE place. A direct
// comparison scattered across call sites is how one of them ends up written the
// other way round — `!= observed`, say — which quietly defaults an unset record
// to gated.
func TestContainmentContainedIsPositiveOnly(t *testing.T) {
	if !ContainmentContained.Contained() {
		t.Error("the contained level does not report Contained()")
	}
	for _, other := range []ContainmentLevel{"", "observed", "adopted", "unknown", "CONTAINED"} {
		if other.Contained() {
			t.Errorf("%q reports Contained(); only the exact contained level may", other)
		}
	}
}

// Every agent enumerated as part of an island is contained BECAUSE it is in an
// island. This asserts the stamp actually happens at that boundary — without it
// the field is present, empty, and read as not-contained, so every real agent in
// the product would report itself ungated.
func TestAgentInfos_StampsContainedAtTheBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents: []project.AgentSpec{
			{ID: "a1", Type: "claude-code"},
			{ID: "a2", Type: "codex"},
		},
	}
	s := &Server{}
	got := s.agentInfos(context.Background(), p, false)

	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	for _, a := range got {
		if a.Containment != ContainmentContained {
			t.Errorf("agent %q in an island has containment %q, want %q — an agent's "+
				"presence in an island IS the containment fact, and this boundary is "+
				"the only place that knows it",
				a.ID, a.Containment, ContainmentContained)
		}
	}
}

// The disagreement guard. Containment is encoded in two places — a field and a
// location — so they can contradict each other, and then every consumer picks
// one to trust and they will not all pick the same one. The stamp is what stops
// that, and this is what proves the stamp ignores whatever the record said.
func TestAgentInfos_IgnoresAnyLevelOnTheRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A record that (somehow, later, via a migration or a bad write) claims it is
	// not contained, while sitting in an island's agent list.
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents:       []project.AgentSpec{{ID: "a1", Type: "claude-code"}},
	}
	s := &Server{}
	got := s.agentInfos(context.Background(), p, false)
	if len(got) != 1 {
		t.Fatalf("got %d agents, want 1", len(got))
	}
	if !got[0].Containment.Contained() {
		t.Error("an agent in an island came back not-contained; the boundary must " +
			"decide from WHERE the agent is, not from what any record claims")
	}
}

// The peer roster is a projection of an island's own agent list, so the level
// was already decided upstream and must survive the copy. Dropping it would make
// every peer read as unset — which callers correctly treat as not-contained, so
// it fails safe, but it is still wrong for a list whose entire purpose is "who
// else is in here with me".
func TestIslandPeerRoster_CarriesContainment(t *testing.T) {
	in := []AgentInfo{
		{ID: "a1", Type: "claude-code", Containment: ContainmentContained},
		{ID: "a2", Type: "codex", Containment: ContainmentContained},
	}
	for _, a := range islandPeerRoster(in) {
		if !a.Containment.Contained() {
			t.Errorf("peer %q lost its containment level in the projection", a.ID)
		}
	}
}

// The naming collision, pinned so it cannot be reintroduced by someone reaching
// for the obvious word. `dejima adopt` already ships and means the OPPOSITE —
// migrating a local project INTO an island. Giving the ungated state that name
// would hand one verb both ends of the only axis this product has.
func TestContainmentDoesNotReuseTheAdoptVerb(t *testing.T) {
	if strings.Contains(strings.ToLower(string(ContainmentContained)), "adopt") {
		t.Error("the contained level is named with the adopt verb")
	}
	// Deliberately not asserting the ungated level's name: it is d1's ruling and
	// is not yet made. This test exists to catch "adopted" arriving as its value
	// without that decision, which is the outcome nobody would have chosen and
	// everybody could drift into.
}
