package main

import "testing"

// The two defaults `confirm` used to conflate are genuinely different questions:
// what to RECOMMEND to a person, and what to DO when nobody is asked.
//
// Local models is the case that forced them apart. Showing an operator [y/N]
// reads as "we advise against this" for a feature that is much of the point of
// a dedicated host — but flipping the flag would have made `--provision-host
// --yes` pull a multi-GB model unattended. Both halves are asserted here
// because fixing either one alone reintroduces the other bug.
func TestConfirmUnattendedSplitsItsTwoDefaults(t *testing.T) {
	t.Run("unattended takes the unattended default, not the interactive one", func(t *testing.T) {
		pc := &provCtx{yes: true}
		if pc.confirmUnattended("Set up local models now?", true, false) {
			t.Error("a --yes run said YES to a multi-GB download; " +
				"the interactive recommendation must not leak into unattended runs")
		}
	})

	t.Run("unattended still honors a true unattended default", func(t *testing.T) {
		pc := &provCtx{yes: true}
		if !pc.confirmUnattended("Something cheap and safe?", true, true) {
			t.Error("defUnattended=true must be honored under --yes")
		}
	})

	t.Run("confirm keeps its old meaning for every existing caller", func(t *testing.T) {
		// confirm now delegates, so both defaults move together — which is what
		// every other call site already expects.
		if (&provCtx{yes: true}).confirm("x", false) {
			t.Error("confirm(false) under --yes must stay false")
		}
		if !(&provCtx{yes: true}).confirm("x", true) {
			t.Error("confirm(true) under --yes must stay true")
		}
	})
}
