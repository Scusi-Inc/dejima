package main

import (
	"strings"
	"testing"
)

func TestFooterTipTextRotates(t *testing.T) {
	m := tuiModel{} // voice is disabled; the pool is stable
	pool := m.footerTips()
	if len(pool) < 2 {
		t.Skip("need at least two tips to test rotation")
	}
	// Advancing the tick counter by tipRotateTicks moves to the next tip.
	m.ticks = 0
	first := m.footerTipText()
	m.ticks = tipRotateTicks
	second := m.footerTipText()
	if first == second {
		t.Errorf("tip should advance after %d ticks, stayed %q", tipRotateTicks, first)
	}
	// A full cycle returns to the start.
	m.ticks = tipRotateTicks * len(pool)
	if got := m.footerTipText(); got != first {
		t.Errorf("tip rotation should wrap to the first tip, got %q want %q", got, first)
	}
}

func TestInsertStringAt(t *testing.T) {
	base := []string{"a", "b", "c"}
	got := insertStringAt(base, 1, "X")
	if strings.Join(got, "") != "aXbc" {
		t.Errorf("insertStringAt mid = %v", got)
	}
	if strings.Join(base, "") != "abc" {
		t.Errorf("insertStringAt must not mutate the caller's slice, got %v", base)
	}
	if got := insertStringAt(base, 99, "Z"); strings.Join(got, "") != "abcZ" {
		t.Errorf("insertStringAt past end should append, got %v", got)
	}
}
