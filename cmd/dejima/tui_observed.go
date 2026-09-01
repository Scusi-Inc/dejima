package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aoos/dejima/internal/api"
)

// Observed agents — the surfaces half of adopt-and-observe (docs/agent-adoption.md).
//
// An observed agent runs OUTSIDE any island. It reads whatever its user can
// read, holds whatever credentials its user holds, and Dejima cannot stop it.
// The design constraint that shapes everything here: it must be a different KIND
// of thing on screen, structurally — not a badge on an island row, not an icon.
// An icon is a banner with fewer words; it relies on the reader noticing and
// remembering, which is exactly what failed when "every agent is walled off from
// the other agents" sat live on six surfaces for weeks.
//
// So this reuses the grammar the dashboard ALREADY has for the other ungated
// thing: host terminals live in their own pinned region above the island list,
// with "· not contained" in the header (renderBand). Copying that is worth more
// than inventing a second treatment — the operator has learned it once, and
// "ungated things live in their own region, above the tree, labelled" becomes a
// rule with two instances rather than a special case with one.
//
// Sibling region rather than merged into the Host band, because the affordances
// genuinely differ: a terminal is a shell the operator opened and can delete; an
// observed agent is something Dejima FOUND and can only watch.

// containmentClaim is the ONE place a surface may obtain a positive containment
// claim. It returns "" for anything that is not contained — including the zero
// value, `observed`, and any level this build does not recognise.
//
// Why a function rather than a vocabulary list of claim phrases: a list catches
// a phrase ALREADY IN IT appearing where it must not, and goes quiet exactly at
// the event it exists for — someone writing a NEW claim literal ("isolated", "in
// an island") into a renderer is, by construction, not editing the list either.
// Routing every claim through one function changes the question from "did the
// author remember the list" to "did the author render a claim at all", which is
// answerable by reading call sites.
//
// It delegates to ContainmentLevel.Contained() rather than comparing levels
// itself: that method exists so the fail-safe reading lives in exactly one
// place, and it already answers false for "", for observed, and for anything
// unrecognised. Restating the comparison here would be a second place for
// someone to write it the other way round.
func containmentClaim(level api.ContainmentLevel) string {
	if !level.Contained() {
		return ""
	}
	return "contained — gated by the Port broker, its crossings ledgered"
}

// observedMsg carries a loaded observed-agent list. gen guards against a
// response that arrives after the operator switched daemons, as listMsg does.
type observedMsg struct {
	gen  int
	resp *api.ObservedAgentsResponse
}

// fetchObservedCmd loads the observed-agent collection.
//
// A FAILURE IS NOT AN EMPTY LIST. On error this returns nothing rather than an
// empty response, because m.observed == nil renders no region at all while an
// empty response renders a searched-and-found-nothing state. A daemon that is
// unreachable, or one too old to serve the endpoint, must not be able to tell
// the operator that no ungated agents exist.
func (m tuiModel) fetchObservedCmd() tea.Cmd {
	if m.client == nil || m.demo {
		return nil
	}
	gen := m.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		resp, err := m.client.ListObservedAgents(ctx)
		if err != nil {
			// Deliberately silent: an older daemon 404s here, and a dashboard that
			// nags about a capability the operator never asked for is noise. The
			// region simply doesn't appear.
			return nil
		}
		return observedMsg{gen: gen, resp: resp}
	}
}

func (m tuiModel) observedRegionVisible() bool { return observedWorthShowing(m.observed) }

// renderObservedRegion draws the pinned "Observed agents" region and its height
// in lines, or ("", 0) when there is nothing honest to say.
func (m tuiModel) renderObservedRegion(width int) (string, int) {
	if !m.observedRegionVisible() {
		return "", 0
	}
	clip := func(s string) string {
		if width > 0 {
			return lipgloss.NewStyle().MaxWidth(width).Render(s)
		}
		return s
	}

	var b strings.Builder
	// The header carries the whole claim: what these are, and that Dejima does
	// not gate them. It is the one line that must survive being skimmed, so the
	// ungated fact rides in the header rather than in a per-row suffix.
	b.WriteString(clip(styleHeader.Render("◇ Observed agents") + " " +
		styleMuted.Render("· not contained · Dejima can see it and cannot stop it")))
	b.WriteString("\n")

	if len(m.observed.Agents) == 0 {
		b.WriteString(clip("  " + styleMuted.Render("none registered")))
		return b.String(), 2
	}

	for _, a := range m.observed.Agents {
		b.WriteString(clip("  " + observedRowText(a)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), len(m.observed.Agents) + 1
}

// observedRowText renders one observed agent.
//
// It reads containment OFF THE RECORD rather than inferring it from the fact
// that the entry arrived in the observed collection. Inferring would re-hide the
// guarantee one layer above where the field fixed it: a surface that reasons
// "this came from the observed list, so it is observed" is one refactor away
// from being handed a merged list and rendering the wrong thing confidently.
//
// So the two encodings — the record's level and the collection it arrived in —
// are allowed to disagree here, and the disagreement is SHOWN. It should never
// happen; that is the point. A row that silently trusts one of them is how it
// would go unnoticed if it did.
func observedRowText(a api.ObservedAgent) string {
	label := a.Label
	if label == "" {
		label = a.ID
	}
	dot := styleMuted.Render("○")
	if a.Alive {
		dot = styleRunning.Render("●")
	}
	// Wider than the island list's 14, because this region spans the whole
	// terminal rather than the narrow left pane — and because the name is the
	// only handle the operator has on a thing Dejima cannot act on. Truncating
	// the identity of an ungated agent to save columns we are not short of is a
	// bad trade.
	const nameW = 28
	line := fmt.Sprintf("%s %s", dot, styleAccent.Render(fmt.Sprintf("%-*s", nameW, truncate(label, nameW))))

	if claim := containmentClaim(a.Containment); claim != "" {
		// The record claims containment while sitting in the collection of things
		// nothing gates. Say so instead of picking a side — and do NOT print the
		// claim itself, which is the one string that must not appear here.
		return line + "  " + styleErrored.Render("⚠ record says contained, but nothing here is gated — report this")
	}

	var parts []string
	if a.Working != "" {
		parts = append(parts, truncate(a.Working, 40))
	}
	if !a.LastActive.IsZero() {
		parts = append(parts, "last active "+timeAgo(a.LastActive)+" ago")
	}
	if a.Source != "" {
		parts = append(parts, "via "+a.Source)
	}
	// Self-reported, and said plainly: everything on this row is what the agent
	// wrote about itself in a transcript Dejima tails. The honest sentence is
	// "the agent reported this", not "this happened".
	parts = append(parts, "self-reported")
	return line + "  " + styleMuted.Render(strings.Join(parts, " · "))
}
