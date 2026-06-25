package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// identityView is the per-island visual-identity editor (opened with `i`): pick
// a color + glyph from the curated palette and persist it (PUT), or clear back
// to the deterministic per-name default. Two axes toggled with Tab. The editor
// only ever emits palette colors + single-rune glyphs, so it can't produce a
// value the backend would reject. Persistence is d5's PUT route — until that
// merges, applying surfaces a "no such route" error and nothing changes.
type identityView struct {
	island   string
	colorSel int
	glyphSel int
	axis     int // identAxisColor | identAxisGlyph
}

const (
	identAxisColor = 0
	identAxisGlyph = 1
)

// openIdentityEditor opens the editor for an island, pre-selected to its current
// visual identity (the stored override if present, else the deterministic
// default for its name).
func (m tuiModel) openIdentityEditor(name string) (tea.Model, tea.Cmd) {
	if name == "" {
		return m, nil
	}
	v := &identityView{island: name}
	curStyle, curGlyph := islandIdentity(name)
	curColor := fmt.Sprint(curStyle.GetForeground())
	if isl, ok := m.islandByName(name); ok && isl.Identity != nil {
		if isl.Identity.Color != "" {
			curColor = isl.Identity.Color
		}
		if isl.Identity.Glyph != "" {
			curGlyph = isl.Identity.Glyph
		}
	}
	for i, c := range islandIdentityColors {
		if fmt.Sprint(c) == curColor {
			v.colorSel = i
		}
	}
	for i, g := range islandIdentityGlyphs {
		if g == curGlyph {
			v.glyphSel = i
		}
	}
	m.identity = v
	return m, nil
}

// identityKey drives the editor: Tab switches axis, ←/→ choose within it, Enter
// applies, x clears to the default, esc cancels.
func (m tuiModel) identityKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.identity
	switch msg.String() {
	case "esc", "q", "i":
		m.identity = nil
	case "tab":
		v.axis = 1 - v.axis
	case "left", "h":
		if v.axis == identAxisColor && v.colorSel > 0 {
			v.colorSel--
		} else if v.axis == identAxisGlyph && v.glyphSel > 0 {
			v.glyphSel--
		}
	case "right", "l":
		if v.axis == identAxisColor && v.colorSel < len(islandIdentityColors)-1 {
			v.colorSel++
		} else if v.axis == identAxisGlyph && v.glyphSel < len(islandIdentityGlyphs)-1 {
			v.glyphSel++
		}
	case "enter":
		island := v.island
		color := string(islandIdentityColors[v.colorSel])
		glyph := islandIdentityGlyphs[v.glyphSel]
		m.identity = nil
		m.dirtyOps[island] = "setting identity"
		return m, m.setIdentityCmd(island, color, glyph)
	case "x":
		// Clear → revert to the deterministic default (empty color+glyph).
		island := v.island
		m.identity = nil
		m.dirtyOps[island] = "clearing identity"
		return m, m.setIdentityCmd(island, "", "")
	}
	return m, nil
}

func (m tuiModel) setIdentityCmd(name, color, glyph string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := c.SetIslandIdentity(ctx, name, color, glyph)
		return opCompleteMsg{name: name, verb: "set identity", err: err}
	}
}

func (m tuiModel) renderIdentityView() string {
	v := m.identity
	selColor := islandIdentityColors[v.colorSel]
	selGlyph := islandIdentityGlyphs[v.glyphSel]
	st := lipgloss.NewStyle().Foreground(selColor)

	var b strings.Builder
	b.WriteString(styleTitle.Render("Visual identity — " + v.island))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("pick a color + glyph (shared for everyone); clear to revert to the auto default"))
	b.WriteString("\n\n")

	// Color swatches: the chosen glyph drawn in each palette color.
	b.WriteString(m.sectionHeader("Color", v.axis == identAxisColor))
	b.WriteString("\n  ")
	for i, c := range islandIdentityColors {
		sw := lipgloss.NewStyle().Foreground(c).Render(selGlyph)
		if i == v.colorSel {
			sw = styleSelected.Render("[" + sw + "]")
		} else {
			sw = " " + sw + " "
		}
		b.WriteString(sw)
	}
	b.WriteString("\n\n")

	// Glyph choices, drawn in the chosen color.
	b.WriteString(m.sectionHeader("Glyph", v.axis == identAxisGlyph))
	b.WriteString("\n  ")
	for i, g := range islandIdentityGlyphs {
		cell := st.Render(g)
		if i == v.glyphSel {
			cell = styleSelected.Render("[" + cell + "]")
		} else {
			cell = " " + cell + " "
		}
		b.WriteString(cell)
	}
	b.WriteString("\n\n")

	b.WriteString(styleMuted.Render("Preview: ") + st.Render(selGlyph+" "+v.island))
	b.WriteString("\n\n")
	b.WriteString(styleMuted.Render("[tab] color/glyph   [←/→] choose   [enter] apply   [x] clear → default   [esc] cancel"))
	return b.String()
}
