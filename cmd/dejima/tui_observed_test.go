package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
)

// observedModel is a dashboard carrying one contained island and whatever
// observed response the case needs.
func observedModel(t *testing.T, resp *api.ObservedAgentsResponse) tuiModel {
	t.Helper()
	m := seededModel(t, island("alpha", "a1"))
	m.observed = resp
	return m
}

func observedResp(agents ...api.ObservedAgent) *api.ObservedAgentsResponse {
	return &api.ObservedAgentsResponse{Agents: agents, Registered: true}
}

// THE SEAM ASSERTION. d3 owns the model, I own the surfaces, and both halves can
// be true — "the field carries the level" and "the surface renders what the
// field says" — while the operator still reads containment off the screen. The
// global claim belongs to neither half, so it is asserted here, over the bytes:
//
//	AN OBSERVED AGENT IS RENDERED, AND NO CONTAINMENT CLAIM IS MADE ABOUT IT.
//
// BOTH HALVES, IN THAT ORDER, IN ONE TEST. The negative alone is satisfied
// perfectly by rendering nothing — an empty view, a fixture that quietly stopped
// producing an observed agent, a region that skipped because a field it needed
// was zero. Every one of those passes for the exact reason the feature would be
// broken. So the agent is asserted present BY NAME first, and the two halves are
// not split across two tests because someone deletes the boring one.
func TestObservedAgentIsRenderedAndNeverClaimedContained(t *testing.T) {
	m := observedModel(t, observedResp(api.ObservedAgent{
		ID: "loose-1", Label: "codex-on-my-laptop",
		Containment: api.ContainmentObserved,
		Alive:       true, Working: "refactoring the parser",
		LastActive: time.Now().Add(-3 * time.Minute), Source: "transcript",
	}))

	screen := plain(m.View())

	// Half one: it is on screen, by name.
	if !strings.Contains(screen, "codex-on-my-laptop") {
		t.Fatalf("the observed agent must be rendered — a screen without it satisfies "+
			"the negative below for the wrong reason:\n%s", screen)
	}
	// ...in its own region, not as a row of the island tree.
	if !strings.Contains(screen, "Observed agents") {
		t.Errorf("observed agents need their own labelled region, not a badge in the list:\n%s", screen)
	}
	if !strings.Contains(screen, "not contained") {
		t.Errorf("the region header must carry the ungated fact where skimming finds it:\n%s", screen)
	}

	// Half two: nothing on that screen claims it is contained.
	assertNoContainmentClaim(t, screen)
}

// assertNoContainmentClaim checks the rendered bytes for every way this UI can
// say the reassuring thing. The claim string itself comes from containmentClaim
// rather than a copy of it, so a reworded claim cannot slip past a guard holding
// a stale duplicate — the phrases beside it are the synonyms a future author
// might reach for instead of the function.
func assertNoContainmentClaim(t *testing.T, screen string) {
	t.Helper()
	claim := containmentClaim(api.ContainmentContained)
	if claim == "" {
		t.Fatal("containmentClaim returns nothing for the contained level, so this guard " +
			"is checking for a string that can never appear — it would pass over anything")
	}

	// ONE KNOWN EXCLUSION, AND IT IS EXCLUDED DELIBERATELY RATHER THAN BY
	// WEAKENING THE LIST. The header tagline (tui.go:3896) reads "Dejima —
	// isolated islands for AI coding agents". It is a claim about ISLANDS, which
	// are isolated, so it stays true with an observed agent on screen — an
	// observed agent is not an island. It is excluded by its exact text, so any
	// OTHER occurrence of "isolated" still fails.
	//
	// The positive control below is the point: if the tagline is reworded, this
	// exclusion stops matching, and rather than silently continuing to strip
	// nothing the guard says so. A stale exclusion that excuses nothing while the
	// new wording goes unexamined is the failure mode of every exemption list.
	const tagline = "isolated islands for AI coding agents"
	if !strings.Contains(screen, tagline) {
		t.Errorf("the header tagline %q is not on screen, so the exclusion below is stale — "+
			"re-check whether the new wording claims containment", tagline)
	}
	screen = strings.ReplaceAll(screen, tagline, "")

	for _, phrase := range []string{claim, "fully contained", "✓ contained", "isolated", "walled off"} {
		if strings.Contains(screen, phrase) {
			t.Errorf("the screen makes a containment claim (%q) with an observed agent on it:\n%s", phrase, screen)
		}
	}
}

// A GOLDEN FOR THE REGION, and the reason it is a golden rather than another
// phrase check is d5's finding: three times now, a sweep for WORDING has missed
// a CLAIM that used none of the searched words. The last one said "each agent
// runs in a container" on a page whose sweep looked for "isolated", "walled off"
// and "from each other" — the claim in full, invisible to the search.
//
// assertNoContainmentClaim has that exact weakness: it catches phrases already
// in its list and cannot catch a new literal, which is the event it exists for.
// containmentClaim() closes the render side by making the claim come from one
// place; this closes the guard side by pinning the region's ENTIRE output, so
// any new string in it — reassuring or not, in any wording — fails until someone
// looks at it and updates this deliberately.
//
// Brittle to cosmetic edits on purpose. This is the one region on screen whose
// job is to not reassure; a copy change here should cost a conversation.
func TestObservedRegionOutputIsPinned(t *testing.T) {
	m := observedModel(t, observedResp(api.ObservedAgent{
		ID: "loose-1", Label: "codex-laptop", Containment: api.ContainmentObserved,
		Alive: true, Working: "refactoring the parser",
		LastActive: time.Now().Add(-3 * time.Minute), Source: "transcript",
	}))
	got, h := m.renderObservedRegion(0)
	const want = "◇ Observed agents · not contained · Dejima can see it and cannot stop it\n" +
		"  ● codex-laptop                  refactoring the parser · last active 3m ago · via transcript · self-reported"
	if plain(got) != want {
		t.Errorf("the observed region's output changed. If that was deliberate, check the new\n"+
			"text makes no containment claim and update this golden.\ngot:\n%s\nwant:\n%s", plain(got), want)
	}
	if h != 2 {
		t.Errorf("region height = %d, want 2 (header + one agent) — the body sizes off this", h)
	}
}

// The zero value is "nobody said". It must render IDENTICALLY to an explicitly
// observed agent as far as containment goes — asserted as an equality rather
// than as two separate absences, because two absences can both hold while the
// outputs differ, and that difference is where "" quietly becomes a third state
// somebody attaches meaning to.
//
// This is the surface's share of d3's zero-value rule: if a stamp is ever missed
// on some path, the screen is the last place that can catch it.
func TestUnstampedObservedAgentRendersAsObserved(t *testing.T) {
	base := api.ObservedAgent{ID: "x1", Label: "loose", Alive: true, Source: "transcript"}

	unstamped := base // Containment == "" — nobody said
	stamped := base
	stamped.Containment = api.ContainmentObserved

	got := plain(observedModel(t, observedResp(unstamped)).View())
	want := plain(observedModel(t, observedResp(stamped)).View())
	if got != want {
		t.Errorf("an unstamped record must render exactly as an observed one, or \"\" has "+
			"become a third state:\nunstamped:\n%s\nobserved:\n%s", got, want)
	}
	assertNoContainmentClaim(t, got)
}

// A record that claims containment while sitting in the collection of things
// nothing gates is the two encodings disagreeing — d3's own noted hazard, since
// both a field and a location encode containment. The row must not pick a side
// silently, and above all must not print the claim.
func TestObservedAgentClaimingContainmentIsFlaggedNotBelieved(t *testing.T) {
	m := observedModel(t, observedResp(api.ObservedAgent{
		ID: "x1", Label: "liar", Containment: api.ContainmentContained, Alive: true,
	}))
	screen := plain(m.View())

	if !strings.Contains(screen, "liar") {
		t.Fatalf("the agent must still be rendered:\n%s", screen)
	}
	if !strings.Contains(screen, "report this") {
		t.Errorf("a record disagreeing with its collection must be surfaced, not resolved silently:\n%s", screen)
	}
	assertNoContainmentClaim(t, screen)
}

// Registration does not exist yet, so an empty list is NOT "we looked and found
// nothing" — it is "there is no way to have told us". Rendering an empty section
// for that claims a completed search, which is a containment claim wearing an
// empty state.
func TestUnregisteredObservedCollectionRendersNothing(t *testing.T) {
	m := observedModel(t, &api.ObservedAgentsResponse{Registered: false})
	if s, h := m.renderObservedRegion(80); s != "" || h != 0 {
		t.Errorf("an unregistered, empty collection must render nothing at all, got (%q, %d)", s, h)
	}

	// A loaded, registered, genuinely empty collection is a real empty state and
	// may say so.
	m2 := observedModel(t, &api.ObservedAgentsResponse{Registered: true})
	s, h := m2.renderObservedRegion(80)
	if h == 0 || !strings.Contains(plain(s), "none registered") {
		t.Errorf("a registered empty collection is a real answer and should say so, got (%q, %d)", plain(s), h)
	}

	// And an agent reported by a daemon that says registration doesn't exist is
	// still shown: failing toward SHOWING an ungated agent is the only safe
	// direction.
	m3 := observedModel(t, &api.ObservedAgentsResponse{
		Registered: false,
		Agents:     []api.ObservedAgent{{ID: "x1", Label: "loose", Containment: api.ContainmentObserved}},
	})
	if s, _ := m3.renderObservedRegion(80); !strings.Contains(plain(s), "loose") {
		t.Errorf("an ungated agent must never be hidden by a registration flag, got %q", plain(s))
	}
}

// A daemon we could not reach, or one too old to serve the endpoint, must not be
// able to tell the operator that no ungated agents exist. nil renders nothing;
// only a LOADED response can produce an empty state.
func TestUnloadedObservedCollectionRendersNothing(t *testing.T) {
	m := seededModel(t, island("alpha", "a1"))
	if m.observed != nil {
		t.Fatal("a fresh model should not have an observed response")
	}
	if s, h := m.renderObservedRegion(80); s != "" || h != 0 {
		t.Errorf("an unloaded collection must render nothing, got (%q, %d)", s, h)
	}
	if strings.Contains(plain(m.View()), "Observed agents") {
		t.Error("the region must not appear before the collection has loaded")
	}
}

// containmentClaim is the one place a surface can obtain a positive claim, so
// everything that is not the contained level must come back empty — including
// levels this build has never heard of.
func TestContainmentClaimOnlySpeaksForContained(t *testing.T) {
	if got := containmentClaim(api.ContainmentContained); got == "" {
		t.Error("the contained level must produce a claim, or every guard using this is vacuous")
	}
	for _, level := range []api.ContainmentLevel{
		"", api.ContainmentObserved, "adopted", "graduated", "CONTAINED", "contained ",
	} {
		if got := containmentClaim(level); got != "" {
			t.Errorf("containmentClaim(%q) = %q, want \"\" — only the contained level may reassure", level, got)
		}
	}
}
