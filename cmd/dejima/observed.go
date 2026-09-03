package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/aoos/dejima/internal/api"
)

// Observed agents on the CLI. The TUI's region is in tui_observed.go; the rule
// that decides whether either of them says anything at all lives HERE, read by
// both, because two surfaces answering "is there anything honest to show" from
// two copies of the same condition is how they end up disagreeing.

// observedWorthShowing reports whether a loaded collection has anything honest
// to render.
//
// nil is NOT an empty collection. It means we never got an answer — an
// unreachable daemon, or one too old to serve the endpoint — and a surface that
// renders "none" for that is telling the operator that no ungated agents exist
// on the strength of a failed request.
//
// Registered:false with no agents is also not an empty collection. Registration
// does not exist yet, so an empty list means "there is no way to have told us",
// not "we looked and found nothing". Rendering an empty section for it claims a
// completed search — a containment claim wearing an empty state, which is the
// same shape as the grants pane's green checkmark one surface over.
//
// If a daemon reports agents while saying registration does not exist, show them
// anyway: failing toward SHOWING an ungated agent is the only safe direction.
func observedWorthShowing(resp *api.ObservedAgentsResponse) bool {
	if resp == nil {
		return false
	}
	return resp.Registered || len(resp.Agents) > 0
}

// fetchObserved loads the observed-agent collection for a CLI command, returning
// nil on any failure. Callers treat nil as "say nothing" — see
// observedWorthShowing. Best-effort by design: an older daemon 404s here, and no
// listing should fail because a post-1.0 capability is absent.
func fetchObserved(ctx context.Context, c *api.Client) *api.ObservedAgentsResponse {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := c.ListObservedAgents(ctx)
	if err != nil {
		return nil
	}
	return resp
}

// printObservedSection writes the observed-agent section beneath an island
// listing, or nothing at all.
//
// A SEPARATE SECTION WITH ITS OWN HEADER, not extra rows in the island table.
// The island table's columns (REPO, STATE, CONTAINER) describe things that live
// in a container; an observed agent has none of them, and filling those cells
// with blanks would put it in the same list as the contained ones — which is the
// arrangement the whole design exists to prevent. Same reasoning as the TUI's
// separate region, and the same words in the header, so an operator moving
// between the two surfaces reads one product.
func printObservedSection(w io.Writer, resp *api.ObservedAgentsResponse) {
	if !observedWorthShowing(resp) {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "OBSERVED AGENTS — not contained. Dejima can see these and cannot stop them.")
	if len(resp.Agents) == 0 {
		fmt.Fprintln(w, "  none registered")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tALIVE\tLAST ACTIVE\tWORKING ON\tSOURCE")
	for _, a := range resp.Agents {
		name := a.Label
		if name == "" {
			name = a.ID
		}
		alive := "no"
		if a.Alive {
			alive = "yes"
		}
		last := "—"
		if !a.LastActive.IsZero() {
			last = timeAgo(a.LastActive) + " ago"
		}
		working := a.Working
		if working == "" {
			working = "—"
		}
		source := a.Source
		if source == "" {
			source = "—"
		}
		// Containment is read off the RECORD, never inferred from the fact that the
		// entry arrived in the observed collection. When the record disagrees with
		// the collection it sits in, say so rather than picking a side — and never
		// print the claim itself.
		if claim := containmentClaim(a.Containment); claim != "" {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", name, alive, last, working, source)
			fmt.Fprintf(tw, "  %s\t\t\t\t\n", "⚠ record says contained, but nothing here is gated — report this")
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", name, alive, last, working, source)
	}
	_ = tw.Flush()
	// Said once, under the table rather than per row: everything above is what
	// each agent wrote about itself in a transcript Dejima tails. The honest
	// sentence is "the agent reported this", not "this happened".
	fmt.Fprintln(w, "  (self-reported — Dejima records what these say about themselves and verifies none of it)")
}
