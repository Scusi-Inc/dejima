package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/hostterm"
)

// bandModel builds a dashboard model with host terminals enabled and a couple
// of terminals loaded.
func bandModel() tuiModel {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 30
	m.overview = &api.OverviewResponse{HostTerminalsEnabled: true}
	m.terminals = []hostterm.Terminal{{ID: "t1", Label: "build"}, {ID: "t2"}}
	return m
}

// TestBandHiddenWhenDisabled: no host-terminal capability → no band at all (so
// the caller adds zero rows and the layout is unchanged).
func TestBandHiddenWhenDisabled(t *testing.T) {
	m := initialTUIModel(nil)
	m.width = 100
	if s, h := m.renderBand(96); s != "" || h != 0 {
		t.Errorf("disabled band should render nothing, got (%q, %d)", s, h)
	}
}

// TestBandCollapsed: one summary line with the count and the expand hotkey.
func TestBandCollapsed(t *testing.T) {
	m := bandModel()
	s, h := m.renderBand(96)
	if h != 1 {
		t.Errorf("collapsed band height = %d, want 1", h)
	}
	bare := plain(s)
	for _, want := range []string{"Host", "2 terminal", "[`] expand"} {
		if !strings.Contains(bare, want) {
			t.Errorf("collapsed band missing %q: %q", want, bare)
		}
	}
}

// TestBandExpanded: lists every terminal + the "+ new terminal" row, height =
// header + n + new.
func TestBandExpanded(t *testing.T) {
	m := bandModel()
	m.bandExpanded = true
	s, h := m.renderBand(96)
	if want := len(m.terminals) + 2; h != want {
		t.Errorf("expanded band height = %d, want %d", h, want)
	}
	bare := plain(s)
	for _, want := range []string{"build", "t2", "+ new terminal", "[`] collapse"} {
		if !strings.Contains(bare, want) {
			t.Errorf("expanded band missing %q: %q", want, bare)
		}
	}
}

// TestTerminalsLeftIslandList: the host terminals no longer flow in the island
// cursor list — they live only in the band now.
func TestTerminalsLeftIslandList(t *testing.T) {
	for _, r := range bandModel().visibleRows() {
		if r.kind == rowTerminal || r.kind == rowNewTerminal {
			t.Fatalf("terminals should not appear in visibleRows anymore: %+v", r)
		}
	}
}

// TestBandToggleKey: backtick expands + focuses the band; esc inside it
// collapses and blurs (auto-collapse on blur).
func TestBandToggleKey(t *testing.T) {
	m := bandModel()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("`")})
	m = out.(tuiModel)
	if !m.bandExpanded || !m.bandFocused {
		t.Fatalf("backtick should expand+focus the band, got expanded=%v focused=%v", m.bandExpanded, m.bandFocused)
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(tuiModel)
	if m.bandExpanded || m.bandFocused {
		t.Errorf("esc should collapse+blur the band, got expanded=%v focused=%v", m.bandExpanded, m.bandFocused)
	}
}

// TestBandAttach: ⏎ on a terminal row sets connectTerminal (the quit-to-attach
// signal main() acts on) and quits; ⏎ on the "+ new" row issues a create.
func TestBandAttach(t *testing.T) {
	m := bandModel()
	m.bandFocused, m.bandExpanded = true, true
	m.bandSel = 1 // second terminal (t2)
	out, cmd := m.bandKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(tuiModel)
	if m.connectTerminal != "t2" {
		t.Errorf("attach should set connectTerminal=t2, got %q", m.connectTerminal)
	}
	if cmd == nil {
		t.Error("attach should return a quit command")
	}

	m = bandModel()
	m.bandFocused, m.bandExpanded = true, true
	m.bandSel = len(m.terminals) // the "+ new terminal" row
	out, cmd = m.bandKey(tea.KeyMsg{Type: tea.KeyEnter})
	if out.(tuiModel).connectTerminal != "" {
		t.Error("the + new row should not set connectTerminal")
	}
	if cmd == nil {
		t.Error("the + new row should issue a create command")
	}
}
