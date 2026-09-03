package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
)

func observedSection(resp *api.ObservedAgentsResponse) string {
	var b bytes.Buffer
	printObservedSection(&b, resp)
	return b.String()
}

// The CLI listing is a second place islands are enumerated, so it gets the same
// treatment: a separate section, the ungated fact in its header, and no
// containment claim anywhere near it.
func TestObservedCLISectionNamesTheAgentAndClaimsNothing(t *testing.T) {
	out := observedSection(&api.ObservedAgentsResponse{
		Registered: true,
		Agents: []api.ObservedAgent{{
			ID: "loose-1", Label: "codex-on-my-laptop",
			Containment: api.ContainmentObserved, Alive: true,
			Working: "refactoring the parser", LastActive: time.Now().Add(-2 * time.Minute),
			Source: "transcript",
		}},
	})

	// Rendered, by name — the positive half, first, for the same reason as the
	// TUI seam test: the negative below is satisfied perfectly by printing
	// nothing, which is what a broken section does.
	if !strings.Contains(out, "codex-on-my-laptop") {
		t.Fatalf("the observed agent must be listed:\n%s", out)
	}
	for _, want := range []string{"OBSERVED AGENTS", "not contained", "self-reported"} {
		if !strings.Contains(out, want) {
			t.Errorf("the section must say %q:\n%s", want, out)
		}
	}
	assertNoContainmentClaimIn(t, out)
}

// assertNoContainmentClaimIn is the CLI's half of the claim scan. It has no
// header tagline to exclude, so it is the stricter of the two.
func assertNoContainmentClaimIn(t *testing.T, out string) {
	t.Helper()
	claim := containmentClaim(api.ContainmentContained)
	if claim == "" {
		t.Fatal("containmentClaim returns nothing for the contained level, so this guard " +
			"is checking for a string that can never appear")
	}
	for _, phrase := range []string{claim, "fully contained", "isolated", "walled off"} {
		if strings.Contains(out, phrase) {
			t.Errorf("the listing makes a containment claim (%q) about an observed agent:\n%s", phrase, out)
		}
	}
}

// The same three refusals as the TUI, asserted on the CLI so the two surfaces
// cannot drift into disagreeing about when there is anything honest to say.
func TestObservedCLISectionSaysNothingWithoutAnAnswer(t *testing.T) {
	// Never loaded (unreachable or older daemon) — silence, not "none".
	if out := observedSection(nil); out != "" {
		t.Errorf("a failed load must print nothing, got %q", out)
	}
	// Registration doesn't exist and nothing is registered: not a completed
	// search, so nothing is printed.
	if out := observedSection(&api.ObservedAgentsResponse{Registered: false}); out != "" {
		t.Errorf("an unregistered empty collection must print nothing, got %q", out)
	}
	// Registered and genuinely empty IS a real answer.
	if out := observedSection(&api.ObservedAgentsResponse{Registered: true}); !strings.Contains(out, "none registered") {
		t.Errorf("a registered empty collection is a real answer and should say so, got %q", out)
	}
	// An agent reported by a daemon that says registration doesn't exist is still
	// shown: never hide an ungated agent behind a flag.
	out := observedSection(&api.ObservedAgentsResponse{
		Registered: false,
		Agents:     []api.ObservedAgent{{ID: "x1", Label: "loose", Containment: api.ContainmentObserved}},
	})
	if !strings.Contains(out, "loose") {
		t.Errorf("an ungated agent must never be hidden by a registration flag, got %q", out)
	}
}

// A record claiming containment inside the collection of things nothing gates is
// the two encodings disagreeing. The CLI flags it rather than believing it, and
// still does not print the claim.
func TestObservedCLIFlagsARecordThatDisagreesWithItsCollection(t *testing.T) {
	out := observedSection(&api.ObservedAgentsResponse{
		Registered: true,
		Agents:     []api.ObservedAgent{{ID: "x1", Label: "liar", Containment: api.ContainmentContained}},
	})
	if !strings.Contains(out, "liar") {
		t.Fatalf("the agent must still be listed:\n%s", out)
	}
	if !strings.Contains(out, "report this") {
		t.Errorf("a record disagreeing with its collection must be surfaced:\n%s", out)
	}
	assertNoContainmentClaimIn(t, out)
}

// Both surfaces decide "is there anything honest to show" from ONE function.
// Asserted directly, because the failure it prevents is the two drifting: a CLI
// that prints "none registered" where the TUI shows nothing is two different
// answers to one question, and only one of them can be right.
func TestBothSurfacesShareTheVisibilityRule(t *testing.T) {
	cases := []*api.ObservedAgentsResponse{
		nil,
		{Registered: false},
		{Registered: true},
		{Registered: false, Agents: []api.ObservedAgent{{ID: "x1"}}},
		{Registered: true, Agents: []api.ObservedAgent{{ID: "x1"}}},
	}
	for _, resp := range cases {
		m := seededModel(t, island("alpha", "a1"))
		m.observed = resp
		tuiShows := m.observedRegionVisible()
		cliShows := observedSection(resp) != ""
		if tuiShows != cliShows {
			t.Errorf("surfaces disagree for %+v: TUI shows=%v, CLI shows=%v", resp, tuiShows, cliShows)
		}
		if tuiShows != observedWorthShowing(resp) {
			t.Errorf("the TUI is not reading the shared rule for %+v", resp)
		}
	}
}
