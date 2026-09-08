package main

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// A terminal too narrow to afford more must behave EXACTLY as it did before.
// This is the whole reason the width is derived rather than "just add 8": the
// rows are clipped to the pane by an ANSI-aware MaxWidth, and the thing at the
// right-hand end of an island row is its status. An unconditional +8 does not
// wrap or error on an 80-column terminal — it silently eats `running · 1.2 GB ·
// 14%` from the right, trading a truncated name for a missing memory readout
// with nothing to say so.
func TestNarrowPanesKeepExactlyTheOldWidth(t *testing.T) {
	for _, paneWidth := range []int{0, -1, 10, 30, 41, 48} {
		if got := nameColumnWidth(paneWidth); got != nameColMin {
			t.Errorf("paneWidth %d gave a name column of %d; a pane that cannot afford "+
				"more must keep the historical %d", paneWidth, got, nameColMin)
		}
	}
}

// And a pane with room spends it on the name, up to a ceiling — island names are
// slugs, and past a point the columns are better spent on status.
func TestWidePanesGrowTheNameColumn(t *testing.T) {
	if got := nameColumnWidth(64); got <= nameColMin {
		t.Errorf("a 64-column pane should grow the name column, got %d", got)
	}
	if got := nameColumnWidth(4000); got != nameColMax {
		t.Errorf("an absurd pane width should stop at the ceiling %d, got %d", nameColMax, got)
	}
	// Monotonic: more room never means a narrower column.
	prev := 0
	for w := 0; w <= 200; w += 7 {
		got := nameColumnWidth(w)
		if got < prev {
			t.Fatalf("name column shrank from %d to %d as the pane grew to %d", prev, got, w)
		}
		prev = got
	}
}

// THE INVARIANT THAT MADE THIS ONE FUNCTION INSTEAD OF TWO. The island name and
// the agent name both start at column 9 and were two independent `%-14s`
// literals that happened to agree. Widening one and not the other would have
// knocked the tree out of alignment, and nothing in the code said they had to
// match — so they are asserted to match here, at several widths.
func TestTheTreeStaysAlignedAtEveryWidth(t *testing.T) {
	for _, paneWidth := range []int{41, 64, 110} {
		m := seededModel(t, island("acme-client-api", "a1", "a2"))
		m.expanded["acme-client-api"] = true
		out, _ := m.renderList(paneWidth)

		var islandRow, agentRow string
		for _, ln := range strings.Split(plain(out), "\n") {
			switch {
			case strings.Contains(ln, "acme") && islandRow == "":
				islandRow = ln
			case strings.Contains(ln, "├ ") && agentRow == "":
				agentRow = ln
			}
		}
		if islandRow == "" || agentRow == "" {
			t.Fatalf("width %d: could not find both rows in:\n%s", paneWidth, plain(out))
		}
		// The status column is what has to line up: it sits immediately after the
		// name column on both row kinds.
		//
		// MEASURED IN DISPLAY COLUMNS, NOT BYTES. The first version of this test
		// used strings.Index directly and reported a misalignment that did not
		// exist: the two rows carry different multi-byte glyphs (▶ ▾ ● ◆ vs ├ ■),
		// so equal columns give unequal byte offsets. The bug was in the ruler.
		col := func(row, needle string) int {
			i := strings.Index(row, needle)
			if i < 0 {
				return -1
			}
			return runewidth.StringWidth(row[:i])
		}
		iCol := col(islandRow, "running")
		aCol := col(agentRow, "claude-code")
		if iCol < 0 || aCol < 0 {
			t.Fatalf("width %d: no status on one of the rows:\n%q\n%q", paneWidth, islandRow, agentRow)
		}
		if iCol != aCol {
			t.Errorf("width %d: island status at column %d, agent meta at %d — the tree is "+
				"out of alignment:\n%q\n%q", paneWidth, iCol, aCol, islandRow, agentRow)
		}
	}
}

// The agent COUNT must never be the thing that gets truncated: "acme-clie… (2)"
// is useful and "acme-client-ap…" with the count cut off is not — the count is
// the only thing on that row saying the island holds more than one agent.
func TestTheAgentCountSurvivesTruncation(t *testing.T) {
	m := seededModel(t, island("a-really-long-island-name", "a1", "a2", "a3"))
	out, _ := m.renderList(41) // narrow: the name must give way, not the count
	if !strings.Contains(plain(out), "(3)") {
		t.Errorf("the agent count was truncated away on a narrow pane:\n%s", plain(out))
	}
	if got := nameColumnAgentCountWidth(41); got != nameColMin-4 {
		t.Errorf("the count costs 4 columns, so the name gets %d, got %d", nameColMin-4, got)
	}
	// Never negative, however absurd the pane.
	if got := nameColumnAgentCountWidth(1); got < 1 {
		t.Errorf("a degenerate pane produced a name width of %d", got)
	}
}

// A wide pane shows the name that a narrow one had to cut. This is the ask,
// asserted as behaviour rather than as arithmetic.
func TestAWidePaneShowsTheWholeIslandName(t *testing.T) {
	const name = "a-really-long-island-name" // 25 chars: cut at 14, whole at 28
	m := seededModel(t, island(name, "a1"))

	narrow, _ := m.renderList(41)
	if strings.Contains(plain(narrow), name) {
		t.Errorf("a 41-column pane cannot fit %q and should have truncated it:\n%s", name, plain(narrow))
	}
	wide, _ := m.renderList(64)
	if !strings.Contains(plain(wide), name) {
		t.Errorf("a 64-column pane has room for %q and did not show it:\n%s", name, plain(wide))
	}
}
