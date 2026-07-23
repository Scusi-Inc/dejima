package main

import (
	"os"
	goruntime "runtime"
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
	for _, want := range []string{"Host", "2 terminal", "[/] expand"} {
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
	for _, want := range []string{"build", "t2", "+ new terminal", "[/] collapse"} {
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

// TestBandFooterAndHelpUseSlash: the footer advertises [/] when host terminals
// are on, the help headlines `/` (not `t`), and the disabled-note fits the
// footer status strip (truncate width) so it never shows an ellipsis.
func TestBandFooterAndHelpUseSlash(t *testing.T) {
	if got := plain(bandModel().renderFooter()); !strings.Contains(got, "[/] host terminal") {
		t.Errorf("footer should advertise [/] host terminal when enabled:\n%s", got)
	}
	help := plain((tuiModel{width: 100}).renderHelp())
	if !strings.Contains(strings.ToLower(help), "host terminals") {
		t.Errorf("help should document the host-terminals key")
	}
	if strings.Contains(help, "new host terminal — an uncontained shell") {
		t.Error("help still headlines the old `t` entry; should headline `/`")
	}
	if len(hostTerminalsOffNote) > 76 {
		t.Errorf("off-note is %d chars; >76 will be truncated in the footer strip", len(hostTerminalsOffNote))
	}
}

// TestBandKeysWhenDisabled: with host terminals off, `/` and `t` are not silent
// — both leave the explanatory note (so the operator isn't left guessing).
func TestBandKeysWhenDisabled(t *testing.T) {
	for _, k := range []string{"/", "t"} {
		m := initialTUIModel(nil)
		m.overview = &api.OverviewResponse{HostTerminalsEnabled: false}
		out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		if got := out.(tuiModel).lastNotice; got != hostTerminalsOffNote {
			t.Errorf("key %q while disabled: note = %q, want the off-note", k, got)
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

// TestBandSlashToggles: `/` opens the band, and `/` again closes it — the same
// key both ways, matching the "[/] collapse" hint. (Regression: `/` used to open
// but not close, leaving the operator stuck in the band.)
func TestBandSlashToggles(t *testing.T) {
	m := bandModel()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = out.(tuiModel)
	if !m.bandExpanded || !m.bandFocused {
		t.Fatalf("/ should expand+focus the band, got expanded=%v focused=%v", m.bandExpanded, m.bandFocused)
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = out.(tuiModel)
	if m.bandExpanded || m.bandFocused {
		t.Errorf("/ again should collapse+blur the band, got expanded=%v focused=%v", m.bandExpanded, m.bandFocused)
	}
}

// TestBandDeleteKeys: d, X, Del, and Backspace on a terminal row all raise the
// remove-terminal confirm; on the "+ new terminal" row none of them do.
func TestBandDeleteKeys(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("d")},
		{Type: tea.KeyRunes, Runes: []rune("X")},
		{Type: tea.KeyDelete},
		{Type: tea.KeyBackspace},
	}
	for _, k := range keys {
		m := bandModel()
		m.bandFocused, m.bandExpanded = true, true
		m.bandSel = 0 // first terminal (t1)
		out, _ := m.bandKey(k)
		c := out.(tuiModel).confirm
		if c == nil || c.verb != "remove-terminal" || c.agent != "t1" {
			t.Errorf("key %v on a terminal should confirm remove-terminal for t1, got %+v", k, c)
		}

		m = bandModel()
		m.bandFocused, m.bandExpanded = true, true
		m.bandSel = len(m.terminals) // the "+ new terminal" row
		out, _ = m.bandKey(k)
		if out.(tuiModel).confirm != nil {
			t.Errorf("key %v on the + new row should not confirm a delete", k)
		}
	}
}

// TestBandAttach: without a new-window backend, ⏎ on a terminal row sets
// connectTerminal (the quit-to-attach fallback main() acts on) and quits; ⏎ on
// the "+ new" row issues a create. canOpenNewWindow is forced false so the
// fallback path is exercised deterministically on every GOOS (otherwise macOS/
// Windows CI would try to exec a real terminal).
func TestBandAttach(t *testing.T) {
	orig := canOpenNewWindow
	canOpenNewWindow = func() bool { return false }
	defer func() { canOpenNewWindow = orig }()

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

// TestBandAttachNewWindow: with a new-window backend available, ⏎ on a terminal
// row opens the host shell in its own window/tab instead of hijacking the
// dashboard — so it neither sets connectTerminal nor quits. (Driven on linux
// without TMUX so openHostTermWindow takes its no-backend branch and returns an
// error rather than exec-ing a real terminal; the contract under test is the
// bandKey decision, not the spawn itself.)
func TestBandAttachNewWindow(t *testing.T) {
	if os.Getenv("TMUX") != "" || goruntime.GOOS != "linux" {
		t.Skip("needs the no-exec branch of openHostTermWindow (linux, no TMUX)")
	}
	orig := canOpenNewWindow
	canOpenNewWindow = func() bool { return true }
	defer func() { canOpenNewWindow = orig }()

	m := bandModel()
	m.bandFocused, m.bandExpanded = true, true
	m.bandSel = 0
	out, cmd := m.bandKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(tuiModel)
	if m.connectTerminal != "" {
		t.Errorf("new-window attach must not set connectTerminal, got %q", m.connectTerminal)
	}
	if cmd != nil {
		t.Error("new-window attach should keep the dashboard up (no quit command)")
	}
}
