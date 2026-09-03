package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTerminalRowIsReachableAndRenders(t *testing.T) {
	var m tuiModel
	m.width, m.height = 100, 40
	m = m.openSettings()
	m.settings.sel = 9 // the Default terminal row

	// Enter must land on the terminal sub-page, not fall through.
	upd, _ := m.settingsKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := upd.(tuiModel)
	if mm.settings == nil {
		t.Fatal("settings closed instead of opening the sub-page")
	}
	if mm.settings.page != settingsTerminal {
		t.Fatalf("row 9 opened page %v, want settingsTerminal", mm.settings.page)
	}

	// The sub-page must actually draw something.
	out := mm.renderSettings()
	if !strings.Contains(out, "default terminal") {
		t.Errorf("the terminal sub-page renders nothing recognisable:\n%s", out)
	}
	for _, want := range []string{"Auto-detect", "Ghostty"} {
		if !strings.Contains(out, want) {
			t.Errorf("choice %q missing from the sub-page:\n%s", want, out)
		}
	}
}

func TestTerminalRowAppearsOnTheTopPage(t *testing.T) {
	var m tuiModel
	m.width, m.height = 100, 40
	m = m.openSettings()
	if out := m.renderSettings(); !strings.Contains(out, "Default terminal") {
		t.Errorf("no Default terminal row on the preferences page:\n%s", out)
	}
	if settingsTopLen != 10 {
		t.Errorf("settingsTopLen = %d; the terminal row makes it 10, and a cursor that "+
			"cannot reach the last row is the row not existing", settingsTopLen)
	}
}
