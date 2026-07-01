package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// TestAgentDisplayNameHonorsShowIDs: the TUI's agentDisplayName delegates to the
// shared agentDisplay, so it's label-only by default and reveals the id only when
// showIDs is on — matching the CLI's --ids/DEJIMA_SHOW_IDS behavior.
func TestAgentDisplayNameHonorsShowIDs(t *testing.T) {
	orig := showIDs
	t.Cleanup(func() { showIDs = orig })

	a := api.AgentInfo{Label: "boss", ID: "p1"}
	showIDs = false
	if got := agentDisplayName(a); got != "boss" {
		t.Errorf("default should be label-only, got %q", got)
	}
	showIDs = true
	if got := agentDisplayName(a); got != "boss (p1)" {
		t.Errorf("with showIDs the id should be revealed, got %q", got)
	}
	// An unlabeled agent always falls back to its id (never blank), either way.
	showIDs = false
	if got := agentDisplayName(api.AgentInfo{ID: "p2"}); got != "p2" {
		t.Errorf("unlabeled agent should show its id, got %q", got)
	}
}

// TestHashTogglesIDs: `#` on the dashboard flips id visibility live and leaves a
// notice, so the operator can reveal ids on demand without relaunching.
func TestHashTogglesIDs(t *testing.T) {
	orig := showIDs
	t.Cleanup(func() { showIDs = orig })
	showIDs = false

	m := initialTUIModel(nil)
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("#")})
	m = out.(tuiModel)
	if !showIDs {
		t.Error("# should reveal ids")
	}
	if !strings.Contains(m.lastNotice, "id") {
		t.Errorf("# should leave a notice about ids, got %q", m.lastNotice)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("#")})
	if showIDs {
		t.Error("# again should hide ids")
	}
}
