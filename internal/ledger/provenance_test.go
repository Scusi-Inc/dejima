package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

// The zero value must not be the reassuring answer. Same rule as the containment
// level: a row nobody stamped must not inherit the strongest claim in the file.
//
// In practice "" appears only on rows written before this field existed, which
// ARE brokered in fact because self-reporting did not exist then. That reasoning
// is true today and rots the moment a new writer forgets to stamp, so the record
// says unknown and the reader is not invited to assume.
func TestProvenanceZeroIsUnknownNotBrokered(t *testing.T) {
	var zero Provenance
	if zero == ProvenanceBrokered {
		t.Fatal("the zero value IS brokered — every unstamped row would claim the " +
			"daemon vouched for it")
	}
	if zero.Verified() {
		t.Error("the zero value reports Verified() — unknown provenance must not read " +
			"as Dejima-verified")
	}
	if string(zero) != "" {
		t.Errorf("zero = %q, want empty so unstamped is distinguishable", zero)
	}
}

// Verified() exists so the cautious reading lives in one place. Scattered
// `!= self-reported` checks are how one renderer ends up treating the unknown
// zero value as trustworthy.
func TestProvenanceVerifiedIsPositiveOnly(t *testing.T) {
	for _, p := range []Provenance{ProvenanceBrokered, ProvenanceWitnessed} {
		if !p.Verified() {
			t.Errorf("%q should report Verified() — Dejima is the source", p)
		}
	}
	for _, p := range []Provenance{"", ProvenanceSelfReported, "unknown", "BROKERED"} {
		if p.Verified() {
			t.Errorf("%q reports Verified(); only Dejima-sourced provenance may", p)
		}
	}
}

// The whole point of the field: a self-reported row must never be mistaken for a
// brokered one. If these collapse, the integrity claim of the entire ledger
// degrades to that of its weakest row.
func TestProvenanceSelfReportedIsDistinct(t *testing.T) {
	if ProvenanceSelfReported == ProvenanceBrokered {
		t.Fatal("self-reported and brokered are the same value")
	}
	if ProvenanceSelfReported.Verified() {
		t.Fatal("self-reported reports Verified() — the subject's own account of " +
			"itself would render as Dejima having vouched for it")
	}
}

// The vocabulary collision guard. `observed` is already the containment level
// for an UNGATED AGENT. A provenance meaning "the daemon saw a contained action"
// under the same word would be the second collision of the kind that was just
// ruled on for adopt/observe — one word, two meanings, on the axis the product
// is about.
func TestProvenanceDoesNotReuseTheObservedVerb(t *testing.T) {
	for _, p := range []Provenance{ProvenanceBrokered, ProvenanceWitnessed, ProvenanceSelfReported} {
		if strings.Contains(strings.ToLower(string(p)), "observ") {
			t.Errorf("provenance %q reuses the observed verb, which already names an "+
				"ungated agent's containment level", p)
		}
	}
}

// Adding a field must not rewrite history. The chain hashes the marshalled
// entry, so a field that serialised on EVERY row — even empty — would change the
// hash of every historical row and break `dejima audit --verify` against an
// existing ledger.
func TestProvenanceOmitsWhenUnsetSoOldChainsStillVerify(t *testing.T) {
	raw, err := json.Marshal(Entry{Type: "trade.read", Island: "isl"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "provenance") {
		t.Errorf("an unstamped entry serialises provenance (%s) — every pre-existing "+
			"row's chain value would change and --verify would fail on a ledger that "+
			"was never tampered with", raw)
	}
	stamped, err := json.Marshal(Entry{Type: "trade.read", Provenance: ProvenanceBrokered})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(stamped), `"provenance":"brokered"`) {
		t.Errorf("a stamped entry does not carry provenance: %s", stamped)
	}
}
