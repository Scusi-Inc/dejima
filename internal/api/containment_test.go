package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/ledger"
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

// The ungated level must NOT report Contained(). This is the assertion the whole
// field exists for: an agent Dejima can see and cannot stop is the case that
// looks identical to a fully locked island from outside, and the one a reader
// must never be reassured about.
func TestContainmentObservedIsNotContained(t *testing.T) {
	if ContainmentObserved.Contained() {
		t.Fatal("the observed level reports Contained() — an agent nothing gates " +
			"would render as gated, which is the exact claim this field exists to prevent")
	}
	if ContainmentObserved == ContainmentContained {
		t.Error("the two levels are the same value")
	}
	if ContainmentObserved == "" {
		t.Error("the observed level IS the zero value — then an unset record would " +
			"claim it, and 'nobody said' would stop being distinguishable from a real answer")
	}
}

// The naming decision, pinned. `dejima adopt` ships and means the OPPOSITE —
// migrating a local project INTO an island. Reusing that verb for the ungated
// state would put a false-containment claim in the vocabulary itself, which is
// worse-placed than any of the four surfaces we removed one from: a sentence can
// be rewritten, a word is what every future surface gets built out of.
func TestContainmentLevelsDoNotReuseTheAdoptVerb(t *testing.T) {
	for _, lvl := range []ContainmentLevel{ContainmentContained, ContainmentObserved} {
		if strings.Contains(strings.ToLower(string(lvl)), "adopt") {
			t.Errorf("level %q is named with the adopt verb, which already means "+
				"the opposite thing in this product", lvl)
		}
	}
}

// --- the enumeration seam --------------------------------------------------

// An observed agent has no island, so it cannot live in IslandInfo.Agents. This
// endpoint is where it lives instead — and the separation is the safety
// property, not a schema preference: every island-keyed surface is unreachable
// from an observed agent because there is no island name to reach it with.
func TestObservedAgents_EmptyIsHonestAboutWhy(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/v1/agents/observed", nil)
	w := httptest.NewRecorder()
	s.handleObservedAgents(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp ObservedAgentsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Agents) != 0 {
		t.Errorf("agents = %+v, want none — nothing can register one yet", resp.Agents)
	}
	// The pair is the point. An empty list ALONE would let a client render "no
	// observed agents" as a finding, when the truth is that Dejima has no way to
	// learn about one. Those are different sentences and only one means it looked.
	if resp.Registered {
		t.Error("registered is true, but no registration path exists — a client " +
			"would present emptiness as a finding")
	}
	// [] not null: a client that distinguishes them will, and null is a third
	// state nobody decided on.
	if !strings.Contains(w.Body.String(), `"agents":[]`) {
		t.Errorf("agents marshalled as null rather than []: %s", w.Body.String())
	}
}

// The stamping rule, mirrored from agentInfos: the level is decided by which
// COLLECTION an agent came from, never read off a record. Containment lives in
// both a field and a location, so if the record could win they would eventually
// disagree and consumers would trust different ones.
func TestStampObserved_OverridesWhateverTheRecordSaid(t *testing.T) {
	in := []ObservedAgent{
		{ID: "a1"}, // unset
		{ID: "a2", Containment: ContainmentContained}, // wrongly claiming containment
		{ID: "a3", Containment: ContainmentObserved},  // already right
	}
	for _, a := range stampObserved(in) {
		if a.Containment != ContainmentObserved {
			t.Errorf("agent %q left the observed collection as %q — the collection "+
				"is the source of this fact, not the record", a.ID, a.Containment)
		}
		if a.Containment.Contained() {
			t.Errorf("agent %q reports Contained() out of the OBSERVED collection", a.ID)
		}
	}
}

// The island-only fields must not exist on an observed agent. Attachable is the
// one that matters: attaching is something Dejima can do to an agent it LAUNCHED
// and cannot do to one it merely watched — and that field drives real affordances
// in five call sites. A field that does not exist cannot be set by a later
// refactor or read by a hopeful renderer.
//
// Asserted by reflection rather than by reading the struct, so the guard covers
// a field added later without anyone remembering to update a list.
func TestObservedAgent_HasNoIslandOnlyFields(t *testing.T) {
	banned := map[string]string{
		"Attachable": "Dejima cannot attach to an agent it did not launch; this field renders an Attach affordance",
		"Tmux":       "an observed agent has no session Dejima owns",
		"Worktree":   "an observed agent has no worktree Dejima created",
		"Branch":     "an observed agent's checkout is not Dejima's to know",
		"Ephemeral":  "spawn lifecycle is an island concept",
		"SpawnedBy":  "spawn lineage is an island concept",
	}
	typ := reflect.TypeOf(ObservedAgent{})
	for i := 0; i < typ.NumField(); i++ {
		if why, bad := banned[typ.Field(i).Name]; bad {
			t.Errorf("ObservedAgent has island-only field %q — %s", typ.Field(i).Name, why)
		}
	}
	// Positive control: the banned list is only meaningful if these names are
	// real. If AgentInfo stops having Attachable, this guard is checking for a
	// field nobody would add anyway and should be revisited rather than trusted.
	ai := reflect.TypeOf(AgentInfo{})
	if _, ok := ai.FieldByName("Attachable"); !ok {
		t.Error("AgentInfo no longer has Attachable — this guard's premise has moved " +
			"and the banned list needs rereading, not extending")
	}
}

// Containment must be on the wire even when unset. A consumer has to read
// containment off the ENTRY, not infer it from which endpoint it arrived on:
// inferring re-hides the guarantee one layer above where the field fixed it, and
// a surface reasoning "this came from the observed list" is one refactor away
// from being handed a merged one.
func TestObservedAgent_ContainmentIsAlwaysOnTheWire(t *testing.T) {
	raw, err := json.Marshal(ObservedAgent{ID: "a1"}) // deliberately unstamped
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"containment"`) {
		t.Errorf("an unstamped observed agent omits containment from the wire (%s) — "+
			"a missing stamp should be VISIBLE, not absent", raw)
	}
}

// ledgerAppend takes provenance as a REQUIRED ARGUMENT, so a writer that forgets
// it does not compile. That is the strongest form of this guarantee anywhere in
// the design — for ContainmentLevel, Go cannot make a struct field required and
// an unstamped record is caught by a test instead. Here the stamp is a
// parameter, so the compiler holds it.
//
// What the compiler CANNOT check is that the argument reaches the entry. This
// asserts that: a caller passing a provenance gets it recorded, rather than the
// parameter being accepted and dropped.
func TestLedgerAppendStampsTheEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := s.ledgerAppend(ledger.ProvenanceWitnessed, ledger.Entry{
		Type: "island.created", Island: "isl",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	lg, err := ledger.Default()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	entries, err := lg.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("nothing was appended")
	}
	got := entries[len(entries)-1]
	if got.Provenance != ledger.ProvenanceWitnessed {
		t.Errorf("provenance = %q, want %q — the parameter is accepted and dropped, "+
			"so every row would carry the unknown zero while the call sites look correct",
			got.Provenance, ledger.ProvenanceWitnessed)
	}
	if got.Provenance.Verified() != true {
		t.Error("a witnessed row does not report Verified()")
	}
}
