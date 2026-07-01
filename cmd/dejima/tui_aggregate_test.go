package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// TestAggregateRenderTotals: a loaded rollup renders the counts + mem/cpu/disk
// totals and NEVER any per-island specifics (it only ever has counts).
func TestAggregateRenderTotals(t *testing.T) {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	m.aggregate = &aggregateView{resp: &api.AggregateResponse{
		TotalIslands: 5, Running: 3, Hibernated: 2,
		MemoryUsageBytes: 2 << 30, MemoryLimitBytes: 8 << 30,
		CPUPercent: 42.5, DiskTotalBytes: 10 << 30,
	}}
	got := plain(m.renderAggregateView())
	for _, want := range []string{"Host utilization", "Islands", "5", "Running", "3", "Memory", "25%", "CPU", "42.5%", "Disk"} {
		if !strings.Contains(got, want) {
			t.Errorf("aggregate render missing %q:\n%s", want, got)
		}
	}
}

// TestAggregateUnavailable: before a1's P3 handler ships the fetch 404s; the
// panel explains what's missing rather than dumping a raw error, and offers
// retry/close.
func TestAggregateUnavailable(t *testing.T) {
	v := &aggregateView{loading: true}
	v.applyLoaded(aggregateLoadedMsg{err: errors.New("http 404")})
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	m.aggregate = v
	got := plain(m.renderAggregateView())
	if !strings.Contains(got, "unavailable") || !strings.Contains(got, "/v1/aggregate") {
		t.Errorf("unavailable panel should name the missing route:\n%s", got)
	}
}

// TestAggregateKeyClose: %/esc close the panel; r refreshes (re-fetches).
func TestAggregateKeyClose(t *testing.T) {
	m := initialTUIModel(nil)
	m.aggregate = &aggregateView{resp: &api.AggregateResponse{}}
	out, _ := m.aggregateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("%")})
	if out.(tuiModel).aggregate != nil {
		t.Error("% should close the aggregate panel")
	}

	m.aggregate = &aggregateView{resp: &api.AggregateResponse{}}
	_, cmd := m.aggregateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil || !m.aggregate.loading {
		t.Error("r should re-fetch (loading + a command)")
	}
}

// TestAggregateOpenKey: `%` on the dashboard opens the panel and kicks a fetch.
func TestAggregateOpenKey(t *testing.T) {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	out, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("%")})
	m = out.(tuiModel)
	if m.aggregate == nil || !m.aggregate.loading {
		t.Error("% should open the aggregate panel in a loading state")
	}
	if cmd == nil {
		t.Error("% should kick a fetch command")
	}
}
