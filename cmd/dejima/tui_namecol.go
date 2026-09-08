package main

// The island/agent name column, sized from the pane rather than pinned at 14.
//
// THE ASK was "show the full island name when possible, or just add about 8
// characters". The second version is the one to avoid, and the reason is worth
// writing down: the list rows are clipped to the pane width by an ANSI-aware
// MaxWidth, and the thing at the RIGHT-HAND END of an island row is its status —
// `running · 1.2 GB · 14%`. Widening the name column unconditionally does not
// wrap or error on a narrow terminal; it silently eats the status column from
// the right. The operator would trade a truncated name for a missing memory
// readout and nothing would say so.
//
// So the width is derived, with a floor at today's 14. A terminal too narrow to
// afford more behaves exactly as it does now — no regression is possible — and
// anything wider spends its slack on the name.
//
// BOTH COLUMNS COME FROM HERE, and that is the point rather than a convenience.
// The island name starts at column 9 (marker, caret, state glyph, identity
// glyph, gap) and the agent name starts at column 9 too (marker, indent, tree
// connector, glyph slot). They were two independent `%-14s` literals that
// happened to agree; widening one and not the other would have knocked the tree
// out of alignment, and nothing in the code said they had to match. Now one
// function answers for both, so they cannot drift.

const (
	// nameColMin is what the column has always been. The floor exists so a narrow
	// terminal keeps today's layout exactly.
	nameColMin = 14
	// nameColMax stops a very wide pane spending everything on names when no
	// island is called anything like that long. Island names are slugs.
	nameColMax = 28
	// nameColLead is the fixed prefix before the name on both row kinds: the
	// selection marker plus each row's own glyph furniture.
	nameColLead = 9
	// nameColStatusReserve keeps room for the widest ORDINARY status,
	// `running · 1.2 GB · 14%`, so growing the name never quietly clips it. A
	// status longer than this (a memory-pressure flag) still clips, exactly as it
	// does today — the reserve protects the common row, not every row.
	nameColStatusReserve = 24
)

// nameColumnWidth returns the shared name-column width for a list pane of the
// given inner width. A non-positive width (a model with no size yet, which
// several tests build) falls back to the floor rather than computing nonsense.
func nameColumnWidth(paneWidth int) int {
	if paneWidth <= 0 {
		return nameColMin
	}
	w := paneWidth - nameColLead - 2 - nameColStatusReserve // 2 = the gap after the name
	if w < nameColMin {
		return nameColMin
	}
	if w > nameColMax {
		return nameColMax
	}
	return w
}

// nameColumnAgentCountWidth is the width available to the NAME on an island row
// that also carries an agent count — ` (3)` costs four columns, and the count
// must never be the thing that gets truncated.
func nameColumnAgentCountWidth(paneWidth int) int {
	w := nameColumnWidth(paneWidth) - 4
	if w < 1 {
		return 1
	}
	return w
}
