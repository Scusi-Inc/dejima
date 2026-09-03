package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/hostterm"
)

// focusModel is a dashboard with BOTH regions populated: islands in the list and
// terminals in the host band. That combination is the whole point — the bug only
// exists when two things can draw a cursor at once.
func focusModel(t *testing.T) tuiModel {
	t.Helper()
	m := seededModel(t, island("alpha", "a1"), island("beta", "b1"))
	m.overview = &api.OverviewResponse{HostTerminalsEnabled: true}
	m.terminals = []hostterm.Terminal{{ID: "t1", Label: "build"}, {ID: "t2"}}
	return m
}

// Pressing [/] hands the keys to the host-terminal band, which draws its own
// highlighted row. The island list must stop drawing a highlighted one: two
// highlighted lines are two claims about where the next keystroke lands, and
// only one of them is true.
//
// Asserted on the GLYPHS, not on styling: lipgloss renders bare under the Ascii
// profile a test harness gets, so a color-only distinction is invisible here —
// an assertion about it would pass whether or not the code did anything.
func TestFocusedBandLeavesOneCursorOnScreen(t *testing.T) {
	m := focusModel(t)

	// Unfocused list: the filled cursor is in the list, and the band is a
	// collapsed one-liner with no cursor of its own.
	list, _ := m.renderList(60)
	if !strings.Contains(plain(list), cursorFocused) {
		t.Errorf("with the list focused it must draw the filled cursor %q: %q", cursorFocused, plain(list))
	}

	// [/] — the band takes the keys.
	res, _ := m.handleKey(key("/"))
	fm := res.(tuiModel)
	if !fm.bandFocused {
		t.Fatal("[/] should focus the host-terminal band")
	}

	list, _ = fm.renderList(60)
	band, _ := fm.renderBand(60)
	bare := plain(list)
	if strings.Contains(bare, cursorFocused) {
		t.Errorf("the list must not keep a filled cursor while the band has the keys: %q", bare)
	}
	if !strings.Contains(bare, cursorBlurred) {
		t.Errorf("the list should keep a hollow cursor %q so the operator can see where they were: %q", cursorBlurred, bare)
	}
	if !strings.Contains(plain(band), "▶") {
		t.Errorf("the focused band should be the one drawing a cursor: %q", plain(band))
	}

	// [/] again hands them back, and the filled cursor returns.
	res, _ = fm.handleKey(key("/"))
	back := res.(tuiModel)
	if back.bandFocused {
		t.Fatal("[/] should toggle the band back off")
	}
	if list, _ := back.renderList(60); !strings.Contains(plain(list), cursorFocused) {
		t.Errorf("blurring the band must restore the list's filled cursor: %q", plain(list))
	}
}

// The cursor is dimmed, never dropped: renderList's second return value drives
// the viewport window, so losing track of the selected row while the band has
// focus would scroll the list out from under the operator and back again when
// they return to it.
func TestBlurringTheListDoesNotMoveItsViewport(t *testing.T) {
	m := focusModel(t)
	m.selected = 1

	_, want := m.renderList(60)
	if want < 0 {
		t.Fatal("a list with a selected row should report its cursor line")
	}

	res, _ := m.handleKey(key("/"))
	fm := res.(tuiModel)
	if _, got := fm.renderList(60); got != want {
		t.Errorf("cursor line moved when the band took focus: got %d, want %d", got, want)
	}
	if fm.selected != m.selected {
		t.Errorf("focusing the band must not move the list selection: got %d, want %d", fm.selected, m.selected)
	}
}
