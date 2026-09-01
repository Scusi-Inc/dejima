package main

import (
	"strings"
	"testing"
)

// THREE SOURCES, named by what the island STARTS FROM.
//
// The pre-scan screen offered five — start empty, scan this directory, choose
// another directory, browse GitHub, enter a URL — which is two questions
// interleaved: what should be in /workspace, and how do I go looking for it.
// The operator's framing was better: clone a repo, use a local one, or start
// with nothing. Finding is a detail inside the first two, not a peer of them.
//
// Before that it was four, with no way to start without a repo at all — and on
// Windows the two directory rows name CLIENT paths a WSL daemon cannot use, so
// none of the four led anywhere the operator could finish.
func TestCreatorOffersThreeSources(t *testing.T) {
	// The REAL rows, not a copy of them. A restated fixture agrees with itself
	// forever: the constants below could drift from the shipping list and this
	// test would still pass.
	c := &creatorModel{rootChoices: rootSourceChoices("this")}
	if len(c.rootChoices) != 3 {
		t.Fatalf("the first screen offers %d sources, want 3", len(c.rootChoices))
	}
	for _, tc := range []struct {
		row  int
		want string
	}{
		{rootRowClone, "clone a repo"},
		{rootRowLocal, "local repo"},
		{rootRowEmpty, "start empty"},
	} {
		if tc.row >= len(c.rootChoices) {
			t.Fatalf("row constant %d is past the end of the list", tc.row)
		}
		// The constants must index the rows they name. An off-by-one runs a
		// different action than the highlighted line, and nothing looks wrong.
		if !strings.Contains(strings.ToLower(c.rootChoices[tc.row]), tc.want) {
			t.Errorf("row %d is %q, expected it to mention %q", tc.row, c.rootChoices[tc.row], tc.want)
		}
	}
}

// Enter-Enter on a fresh client must not do something surprising. The cursor's
// zero value decides the default action (#355). Clone leads because it is the
// common case AND the only one of the three that cannot be reached later from
// inside the others — but the choice is made explicitly, so reordering the rows
// cannot silently change what the default does.
func TestCreatorDefaultRowIsClone(t *testing.T) {
	if rootRowClone != 0 {
		t.Errorf("rootRowClone = %d; the cursor's zero value is what Enter-Enter runs", rootRowClone)
	}
}

// Each row carries a leading glyph, and every glyph is SINGLE-WIDTH.
//
// Emoji are double-width in most terminals and inconsistently so across them.
// A double-width glyph in one row and not another shifts that row's muted
// description out of column — on the operator's machine, which we never see.
func TestSourceRowsHaveSingleWidthIcons(t *testing.T) {
	want := []rune{'\u21e3', '\u2302', '\u25cc'}
	rows := rootSourceChoices("this")
	for i, r := range rows {
		icon := []rune(r)[0]
		if icon != want[i] {
			t.Errorf("row %d starts with %q, want %q", i, icon, want[i])
		}
		// Anything outside the BMP is emoji or worse. Codepoints above U+FFFF
		// are the ones terminals render double-width.
		if icon > 0xFFFF {
			t.Errorf("row %d icon %q is outside the BMP; it will render double-width and break alignment", i, icon)
		}
	}
	// All three descriptions must start at the same column, or the list looks
	// ragged. Compare where the muted run begins, by rune not byte.
	col := -1
	for i, r := range rows {
		at := strings.Index(r, "browse")
		if at < 0 {
			at = strings.Index(r, "a git repo")
		}
		if at < 0 {
			at = strings.Index(r, "no repo")
		}
		if at < 0 {
			t.Fatalf("row %d has no recognisable description: %q", i, r)
		}
		// Styling can wrap the description, so measure the VISIBLE prefix only —
		// counting raw bytes would count escape sequences as columns.
		runes := len([]rune(stripANSI(r[:at])))
		if col == -1 {
			col = runes
		} else if runes != col {
			t.Errorf("row %d description starts at column %d, row 0 starts at %d — the list is ragged", i, runes, col)
		}
	}
}

// stripANSI removes escape sequences so column math counts what a person sees.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
