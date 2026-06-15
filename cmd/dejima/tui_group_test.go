package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func islandNames(in []api.IslandInfo) []string {
	out := make([]string, len(in))
	for i, isl := range in {
		out[i] = isl.Name
	}
	return out
}

func TestOrderedIslandsGrouping(t *testing.T) {
	m := tuiModel{islands: []api.IslandInfo{
		{Name: "web-1", Repo: "r/web"},
		{Name: "api-1", Repo: "r/api"},
		{Name: "web-2", Repo: "r/web"},
		{Name: "loose"}, // no repo
	}}

	// Ungrouped: order is untouched.
	if got := islandNames(m.orderedIslands()); got[0] != "web-1" || got[1] != "api-1" {
		t.Errorf("ungrouped order changed: %v", got)
	}

	// Grouped: same-repo islands become contiguous, first-seen repo order kept.
	m.grouped = true
	got := islandNames(m.orderedIslands())
	want := []string{"web-1", "web-2", "api-1", "loose"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("grouped order = %v, want %v", got, want)
		}
	}

	// The flattened row list reflects the grouped order (island rows only).
	var islandRows []string
	for _, r := range m.visibleRows() {
		if r.kind == rowIsland {
			islandRows = append(islandRows, r.island)
		}
	}
	for i := range want {
		if islandRows[i] != want[i] {
			t.Fatalf("grouped rows = %v, want %v", islandRows, want)
		}
	}
}
