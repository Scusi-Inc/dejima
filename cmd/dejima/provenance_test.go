package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/ledger"
)

func entryWith(p ledger.Provenance) ledger.Entry {
	return ledger.Entry{Seq: 1, Type: "trade.read", Island: "alpha", Provenance: p}
}

// The requirement, in one test: a self-reported row and a brokered row must
// never be indistinguishable. If they are, the ledger's integrity claim degrades
// to that of its weakest row — and the ledger is what gets shown to a team lead.
func TestSelfReportedRowsAreMarkedAndBrokeredOnesAreNot(t *testing.T) {
	if got := provenanceMark(ledger.ProvenanceBrokered); got != "" {
		t.Errorf("a brokered row must be unmarked — marks are here to be rare; got %q", got)
	}
	if got := provenanceMark(ledger.ProvenanceWitnessed); got != "" {
		t.Errorf("a witnessed row is Dejima's own observation and needs no mark; got %q", got)
	}
	selfReported := provenanceMark(ledger.ProvenanceSelfReported)
	if selfReported == "" {
		t.Fatal("a self-reported row MUST be distinguishable from a brokered one")
	}

	// The zero value is unknown, not brokered — so it is marked too, but not with
	// the same mark: "nobody stamped this" and "the subject wrote it" are
	// different claims and must not collapse into one symbol.
	unknown := provenanceMark("")
	if unknown == "" {
		t.Error(`the unstamped zero value must not render as vouched-for — "" means nobody said`)
	}
	if unknown == selfReported {
		t.Errorf("unknown and self-reported share the mark %q; they are different claims", unknown)
	}
	// A level from a newer daemon that this build has never heard of is also not
	// vouched for. Fail-safe: unrecognised must never read as verified.
	if provenanceMark("notarised-by-a-future-daemon") == "" {
		t.Error("an unrecognised provenance must not render as vouched-for")
	}
}

// "✓ hash chain intact" is the strongest sentence the audit pane says, and it is
// true of the CHAIN — nobody edited these rows. It says nothing about whether a
// row was true when written. A self-reported row under an unqualified banner
// borrows the chain's assurance for a claim the chain never made.
func TestTheChainBannerIsQualifiedWhenARowIsNotVouchedFor(t *testing.T) {
	allBrokered := []ledger.Entry{entryWith(ledger.ProvenanceBrokered), entryWith(ledger.ProvenanceWitnessed)}
	if note := chainNote(allBrokered); note != "" {
		t.Errorf("an all-vouched-for ledger needs no qualifier — it would be noise; got %q", note)
	}

	for _, p := range []ledger.Provenance{ledger.ProvenanceSelfReported, "", "something-new"} {
		mixed := append(append([]ledger.Entry{}, allBrokered...), entryWith(p))
		note := chainNote(mixed)
		if note == "" {
			t.Errorf("provenance %q on screen must qualify the chain banner", p)
			continue
		}
		if !strings.Contains(note, "not that every one of them happened") {
			t.Errorf("the qualifier must separate 'was not edited' from 'happened'; got %q", note)
		}
	}
}

// The legend has to name WHICH weak claim is present, because the remedies
// differ: an unstamped row is a record written before provenance existed, while
// a self-reported one is the subject's own account and omission from it leaves
// no trace at all.
func TestTheLegendDistinguishesSelfReportedFromUnstamped(t *testing.T) {
	if note := provenanceNote([]ledger.Entry{entryWith(ledger.ProvenanceBrokered)}); note != "" {
		t.Errorf("nothing to explain in an all-brokered ledger; got %q", note)
	}

	self := provenanceNote([]ledger.Entry{entryWith(ledger.ProvenanceSelfReported)})
	if !strings.Contains(self, "self-reported") || !strings.Contains(self, "Dejima did not see it") {
		t.Errorf("the self-reported legend must say Dejima did not see it; got %q", self)
	}
	if strings.Contains(self, "unstamped") {
		t.Errorf("a purely self-reported ledger must not mention unstamped rows; got %q", self)
	}

	unknown := provenanceNote([]ledger.Entry{entryWith("")})
	if !strings.Contains(unknown, "unstamped") {
		t.Errorf("the unstamped legend must name what it is; got %q", unknown)
	}
	if strings.Contains(unknown, "the agent's own account") {
		t.Errorf("an unstamped row is not the same claim as a self-reported one; got %q", unknown)
	}

	both := provenanceNote([]ledger.Entry{entryWith(ledger.ProvenanceSelfReported), entryWith("")})
	if !strings.Contains(both, "self-reported") || !strings.Contains(both, "unstamped") {
		t.Errorf("a mixed ledger must name both; got %q", both)
	}
	if !strings.Contains(both, "1 self-reported") || !strings.Contains(both, "1 unstamped") {
		t.Errorf("the legend should count each kind so the scale is visible; got %q", both)
	}
}

// Both audit surfaces read one rule. A CLI that marks a row where the TUI does
// not is two answers to the question the ledger exists to answer.
func TestBothAuditSurfacesShareTheProvenanceRule(t *testing.T) {
	m := seededModel(t, island("alpha", "a1"))
	for _, p := range []ledger.Provenance{
		ledger.ProvenanceBrokered, ledger.ProvenanceWitnessed,
		ledger.ProvenanceSelfReported, "", "future-level",
	} {
		e := entryWith(p)
		m.audit = &auditView{entries: []ledger.Entry{e}, verified: true, total: 1, returned: 1}
		pane := plain(m.renderAuditView())

		marked := provenanceMark(p) != ""
		// The TUI's row must carry the same mark the shared rule produces...
		if marked && !strings.Contains(pane, provenanceMark(p)) {
			t.Errorf("provenance %q: the pane does not show the mark %q:\n%s", p, provenanceMark(p), pane)
		}
		// ...and the banner qualifier must follow the same rule, in both directions.
		qualified := strings.Contains(pane, "not that every one of them happened")
		if qualified != marked {
			t.Errorf("provenance %q: marked=%v but banner qualified=%v — the two must agree:\n%s",
				p, marked, qualified, pane)
		}
	}
}
