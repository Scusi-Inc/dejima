package main

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

// dashboardTips must fit one terminal line beside the logo — cap at the tagline
// width (~68 display columns) so they don't wrap or clip on a normal terminal.
func TestDashboardTipsFitWidth(t *testing.T) {
	const maxCols = 68
	if len(dashboardTips) == 0 {
		t.Fatal("expected some tips")
	}
	for _, tip := range dashboardTips {
		if w := runewidth.StringWidth(tip); w > maxCols {
			t.Errorf("tip too wide (%d > %d cols): %q", w, maxCols, tip)
		}
	}
}

// currentTip rotates through every tip and never panics on any tick.
func TestCurrentTipRotates(t *testing.T) {
	seen := map[string]bool{}
	for tick := 0; tick < len(dashboardTips)*6+6; tick++ {
		seen[currentTip(tick)] = true
	}
	if len(seen) != len(dashboardTips) {
		t.Errorf("rotation covered %d tips, want %d", len(seen), len(dashboardTips))
	}
	if currentTip(-42) == "" {
		t.Error("currentTip must handle a negative tick without returning empty")
	}
}
