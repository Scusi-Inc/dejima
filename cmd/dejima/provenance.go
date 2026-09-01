package main

import (
	"fmt"

	"github.com/aoos/dejima/internal/ledger"
)

// How the ledger's three provenance levels are shown, on both the TUI pane and
// `dejima audit`. One file, read by both, because two surfaces answering "can
// Dejima vouch for this row" from two copies of the same rule is how they end up
// disagreeing about the one thing the ledger is for.
//
// The requirement (docs/agent-adoption.md, constraint 4): a self-reported row
// and a brokered row must never be indistinguishable. "Here is an audit trail"
// and "here is an audit trail, some rows of which are the subject's own account
// of itself" are different products, and the ledger is what gets shown to a team
// lead.

// provenanceMark is the per-row marker: "" for a row Dejima can vouch for, and a
// visible mark for the two it cannot.
//
// VERIFIED ROWS ARE UNMARKED, deliberately. Marking every row would make the
// column decoration, and the eye stops reading a column that always says the
// same thing — the marks are here to be rare. It also keeps the common ledger,
// which is entirely brokered, looking exactly as it does today.
//
// The unknown zero value gets a QUIETER mark than self-reported, because they
// are different claims: "" means nobody stamped this (in practice, an entry
// written before the field existed, which is brokered in fact), while
// self-reported means the subject wrote it and Dejima did not see it. Rendering
// unknown as alarming would cry wolf across every legacy row; rendering it as
// verified would be the zero value quietly reassuring, which is the thing the
// level was designed not to do.
func provenanceMark(p ledger.Provenance) string {
	switch {
	case p.Verified():
		return ""
	case p == ledger.ProvenanceSelfReported:
		return "⚠"
	default:
		return "?"
	}
}

// provenanceNote explains a mark in one line, for the legend under a table that
// contains one. Empty when nothing needs explaining.
func provenanceNote(entries []ledger.Entry) string {
	selfReported, unknown := 0, 0
	for _, e := range entries {
		switch {
		case e.Provenance.Verified():
		case e.Provenance == ledger.ProvenanceSelfReported:
			selfReported++
		default:
			unknown++
		}
	}
	switch {
	case selfReported > 0 && unknown > 0:
		return fmt.Sprintf("⚠ %d self-reported (the agent's own account — Dejima did not see it) · ? %d unstamped (provenance unknown)",
			selfReported, unknown)
	case selfReported > 0:
		return fmt.Sprintf("⚠ %d self-reported (the agent's own account — Dejima did not see it, and omission leaves no trace)", selfReported)
	case unknown > 0:
		return fmt.Sprintf("? %d unstamped (written before provenance was recorded; provenance unknown)", unknown)
	}
	return ""
}

// chainNote qualifies the chain-verification banner when the rows on screen are
// not all vouched for.
//
// THIS IS THE POINT OF THE WHOLE FILE. "✓ hash chain intact" is the strongest
// sentence this pane says, and it is true of the CHAIN — nobody edited these
// rows after they were written. It says nothing about whether a row was true
// when written. A self-reported row inside an intact chain is a tamper-proof
// record of something the subject claimed, and an operator who reads the banner
// and the row together, without this sentence between them, walks away with the
// chain's assurance attached to the row's claim.
//
// That is exactly how "the integrity claim of the whole ledger degrades to its
// weakest row" happens: not by anyone lying, but by a strong true sentence
// sitting above a weak true one.
func chainNote(entries []ledger.Entry) string {
	for _, e := range entries {
		if !e.Provenance.Verified() {
			return "the chain proves these rows were not edited, not that every one of them happened"
		}
	}
	return ""
}
