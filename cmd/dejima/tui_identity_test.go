package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// TestIslandVisualReadThrough: a stored override wins; otherwise the read-through
// matches the deterministic per-name default exactly.
func TestIslandVisualReadThrough(t *testing.T) {
	// No override → identical to islandIdentity(name).
	plainIsl := api.IslandInfo{Name: "myrepo"}
	gotStyle, gotGlyph := islandVisual(plainIsl)
	wantStyle, wantGlyph := islandIdentity("myrepo")
	if gotGlyph != wantGlyph || fmt.Sprint(gotStyle.GetForeground()) != fmt.Sprint(wantStyle.GetForeground()) {
		t.Errorf("no-override islandVisual should equal the default, got (%v,%q)", gotStyle.GetForeground(), gotGlyph)
	}
	// Override → exactly the stored color + glyph.
	ovr := api.IslandInfo{Name: "myrepo", Identity: &api.IslandIdentity{Color: "#abcdef", Glyph: "★"}}
	st, g := islandVisual(ovr)
	if g != "★" || fmt.Sprint(st.GetForeground()) != "#abcdef" {
		t.Errorf("override islandVisual = (%v,%q), want (#abcdef,★)", st.GetForeground(), g)
	}
	// A half-set override (missing glyph) is ignored → default.
	half := api.IslandInfo{Name: "myrepo", Identity: &api.IslandIdentity{Color: "#abcdef"}}
	if _, g := islandVisual(half); g != wantGlyph {
		t.Errorf("half-set override should fall back to the default glyph, got %q", g)
	}
}

// TestIdentityEditorFlow: opening pre-selects, Tab/→ navigate, Enter applies a
// palette value, x clears.
func TestIdentityEditorFlow(t *testing.T) {
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	base := func() tuiModel {
		m := initialTUIModel(nil)
		m.islands = []api.IslandInfo{{Name: "myrepo"}}
		return m
	}

	// Open → editor present, bound to the island.
	out, _ := base().openIdentityEditor("myrepo")
	m := out.(tuiModel)
	if m.identity == nil || m.identity.island != "myrepo" {
		t.Fatalf("openIdentityEditor should open for myrepo, got %+v", m.identity)
	}

	// Tab switches axis (color → glyph).
	m, _ = m.identityKeyModel(tea.KeyMsg{Type: tea.KeyTab}, t)
	if m.identity.axis != identAxisGlyph {
		t.Errorf("Tab should switch the axis to glyph, got %d", m.identity.axis)
	}

	// Enter applies — fires a command, clears the overlay, marks the island dirty.
	mm := base()
	mm, _ = mm.openIdentityEditorModel("myrepo")
	mm.identity.colorSel, mm.identity.glyphSel = 1, 2
	out, cmd := mm.identityKey(tea.KeyMsg{Type: tea.KeyEnter})
	res := out.(tuiModel)
	if cmd == nil {
		t.Error("Enter should fire a set-identity command")
	}
	if res.identity != nil {
		t.Error("Enter should close the editor")
	}
	if res.dirtyOps["myrepo"] == "" {
		t.Error("Enter should mark the island dirty")
	}

	// x clears (also fires a command, closes).
	mm = base()
	mm, _ = mm.openIdentityEditorModel("myrepo")
	out, cmd = mm.identityKey(key("x"))
	if cmd == nil || out.(tuiModel).identity != nil {
		t.Error("x should clear (fire a command) and close the editor")
	}
}

// TestIdentityRender shows the island, both axes, a preview, and the footer.
func TestIdentityRender(t *testing.T) {
	m := tuiModel{width: 100, identity: &identityView{island: "myrepo", colorSel: 0, glyphSel: 0}}
	out := plain(m.renderIdentityView())
	for _, want := range []string{"Visual identity — myrepo", "Color", "Glyph", "Preview", "[enter] apply", "clear"} {
		if !strings.Contains(out, want) {
			t.Errorf("identity render missing %q:\n%s", want, out)
		}
	}
}

// helpers that return the concrete model for chaining.
func (m tuiModel) openIdentityEditorModel(name string) (tuiModel, tea.Cmd) {
	out, c := m.openIdentityEditor(name)
	return out.(tuiModel), c
}
func (m tuiModel) identityKeyModel(msg tea.KeyMsg, t *testing.T) (tuiModel, tea.Cmd) {
	t.Helper()
	out, c := m.identityKey(msg)
	return out.(tuiModel), c
}
