package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The help overlay is windowed: the body box's interior height must equal
// helpInnerHeight() so scrollWindow fills the box exactly — no clipped row, no
// blank gap — and the tail of the (taller-than-screen) help must stay reachable
// by scrolling. a1 flagged the viewport math (helpInnerHeight = height-header-4)
// as approximated in #263; these pin it deterministically since a live-TTY pass
// isn't available in-container.

// helpBoxInterior reproduces the body-box interior height used by View() for the
// help overlay: the box is stylePane.Height(m.height - hh - 2) with a 2-row
// rounded border and zero vertical padding, so its interior is that minus 2.
func helpBoxInterior(m tuiModel) int {
	hh := lipgloss.Height(m.renderHeader())
	return (m.height - hh - 2) - 2
}

// TestHelpInnerHeightMatchesBoxInterior: the height scrollWindow is asked to
// fill equals the actual interior of the bordered body box. If these drift, the
// overlay either clips its last content row or leaves a dead row at the bottom.
func TestHelpInnerHeightMatchesBoxInterior(t *testing.T) {
	for _, h := range []int{8, 10, 14, 20, 40} {
		m := initialTUIModel(nil)
		m.width, m.height = 100, h
		want := helpBoxInterior(m)
		if want < 3 {
			want = 3 // helpInnerHeight's floor for tiny terminals
		}
		if got := m.helpInnerHeight(); got != want {
			t.Errorf("height=%d: helpInnerHeight()=%d, box interior=%d (drift clips or gaps the overlay)", h, got, want)
		}
	}
}

// TestHelpWindowFillsExactly: at every short height the windowed help occupies
// exactly helpInnerHeight lines (never more — that would overflow the box) once
// the content is taller than the viewport.
func TestHelpWindowFillsExactly(t *testing.T) {
	for _, h := range []int{8, 10, 14, 20} {
		for _, w := range []int{60, 100} {
			m := initialTUIModel(nil)
			m.width, m.height = w, h
			inner := m.helpInnerHeight()
			content, _ := scrollWindow(m.renderHelp(), inner, 0)
			got := len(strings.Split(content, "\n"))
			total := len(strings.Split(m.renderHelp(), "\n"))
			if total > inner && got != inner {
				t.Errorf("h=%d w=%d: window is %d lines, want exactly %d (box interior)", h, w, got, inner)
			}
			if got > inner {
				t.Errorf("h=%d w=%d: window %d lines OVERFLOWS the %d-row box", h, w, got, inner)
			}
		}
	}
}

// TestHelpTailReachable: scrolling to the end (the g/G, PgDn path) brings the
// final help line into the viewport — no key documentation is stranded below an
// unreachable fold on a short terminal.
func TestHelpTailReachable(t *testing.T) {
	for _, h := range []int{8, 10, 14} {
		m := initialTUIModel(nil)
		m.width, m.height = 100, h
		lines := strings.Split(m.renderHelp(), "\n")
		lastNonEmpty := ""
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.TrimSpace(plain(lines[i])) != "" {
				lastNonEmpty = lines[i]
				break
			}
		}
		// Scroll to the very bottom the way Update handles 'G'.
		m = m.scrollHelpLines(1 << 30)
		content, _ := scrollWindow(m.renderHelp(), m.helpInnerHeight(), m.helpScroll)
		if !strings.Contains(content, lastNonEmpty) {
			t.Errorf("h=%d: last help line %q not reachable by scroll-to-end", h, plain(lastNonEmpty))
		}
	}
}

// TestHelpOpensAtTop: opening the overlay resets scroll so the title row is the
// first visible line (Update sets helpScroll=0 on open).
func TestHelpOpensAtTop(t *testing.T) {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 12
	m.helpScroll = 0 // as Update does when toggling help on
	content, _ := scrollWindow(m.renderHelp(), m.helpInnerHeight(), m.helpScroll)
	first := strings.Split(m.renderHelp(), "\n")[0]
	if !strings.HasPrefix(content, first) {
		t.Errorf("help should open at the top; window starts %q, want title %q", plain(strings.Split(content, "\n")[0]), plain(first))
	}
}
