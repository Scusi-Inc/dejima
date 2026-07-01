package main

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// TestHelpFitsWidth: every help line stays within the pane's content width
// (m.width - 4) at a range of terminal sizes, so a narrow terminal clips with
// an … instead of wrapping (a wrap would desync the scroll window's line count).
func TestHelpFitsWidth(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100, 120} {
		m := initialTUIModel(nil)
		m.width, m.height = w, 50
		budget := w - 4
		if budget < 24 {
			budget = 24 // matches renderHelp's floor
		}
		for i, line := range strings.Split(plain(m.renderHelp()), "\n") {
			if got := runewidth.StringWidth(line); got > budget {
				t.Errorf("width=%d: line %d is %d cols (budget %d): %q", w, i, got, budget, line)
			}
		}
	}
}

// TestHelpTruncatesWithEllipsis: at a genuinely narrow width the long rows are
// clipped with an ellipsis rather than dropped or wrapped.
func TestHelpTruncatesWithEllipsis(t *testing.T) {
	m := initialTUIModel(nil)
	m.width, m.height = 50, 50
	if !strings.Contains(plain(m.renderHelp()), "…") {
		t.Error("a 50-col help overlay should clip long rows with an ellipsis")
	}
}

// TestTruncateDisplayWideRunes: the truncator measures by display width and
// never splits a multibyte rune.
func TestTruncateDisplayWideRunes(t *testing.T) {
	if got := truncateDisplay("hello world", 8); got != "hello w…" {
		t.Errorf("plain truncate = %q, want %q", got, "hello w…")
	}
	// Multibyte middots must not be sliced mid-rune; result stays valid UTF-8
	// and within budget.
	got := truncateDisplay("a · b · c · d · e", 7)
	if runewidth.StringWidth(got) > 7 {
		t.Errorf("truncated %q is %d cols, want <=7", got, runewidth.StringWidth(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected an ellipsis suffix, got %q", got)
	}
	// No clipping when it already fits.
	if got := truncateDisplay("short", 20); got != "short" {
		t.Errorf("fitting string should be unchanged, got %q", got)
	}
	if got := truncateDisplay("anything", 0); got != "" {
		t.Errorf("zero width should yield empty, got %q", got)
	}
}
