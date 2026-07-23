package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/charmbracelet/lipgloss"
)

// TestIslandIdentity: the per-island color+glyph is stable for a given name,
// the UNIFORM default — white + the neutral glyph — the same for every island
// (distinct color/glyph is opt-in via the editor, not auto-assigned per name).
func TestIslandIdentity(t *testing.T) {
	colors := map[string]bool{}
	glyphs := map[string]bool{}
	for _, n := range []string{"alpha", "bravo", "charlie", "delta", "echo", "myrepo", "infra", "web", "api", "data", "x", "Port"} {
		st, g := islandIdentity(n)
		colors[fmt.Sprint(st.GetForeground())] = true
		glyphs[g] = true
	}
	// Uniform: every island gets the same default color + glyph, regardless of name.
	if len(colors) != 1 || len(glyphs) != 1 {
		t.Errorf("default identity should be uniform, got %d colors / %d glyphs", len(colors), len(glyphs))
	}
	st, g := islandIdentity("anything")
	if fmt.Sprint(st.GetForeground()) != islandIdentityDefaultColor {
		t.Errorf("default color = %v, want %s", st.GetForeground(), islandIdentityDefaultColor)
	}
	if g != islandIdentityDefaultGlyph {
		t.Errorf("default glyph = %q, want %q", g, islandIdentityDefaultGlyph)
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// sampleModel builds a dashboard with a mix of agent kinds and states so the
// glyph vocabulary can be rendered and asserted in one place.
func sampleModel() tuiModel {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 30
	m.expanded["myrepo"] = true
	m.islands = []api.IslandInfo{{
		Name:      "myrepo",
		Agent:     "claude-code",
		Container: "running",
		Agents: []api.AgentInfo{
			{ID: "a1", Type: "claude-code", Attachable: true, State: "running",
				CreatedAt:  time.Now().Add(-90 * time.Minute),
				Attached:   []api.PresenceEntry{{Label: "austin", JoinedAt: time.Unix(0, 0)}},
				AgentState: &api.AgentStateInfo{Latest: "waiting-for-input", UpdatedAt: time.Unix(0, 0)}},
			{ID: "a2", Type: "codex", Label: "Backend", Attachable: true, State: "stopped"},
			{ID: "a3", Type: "headless", Attachable: false, State: "running", Error: "worktree add failed"},
		},
	}}
	return m
}

// TestAgentGlyphKind locks in shape = identity: AI agents get the diamond, a
// plain shell gets the prompt, headless gets the box — independent of state.
func TestAgentGlyphKind(t *testing.T) {
	agent := api.AgentInfo{Type: "claude-code", Attachable: true, State: "running"}
	shell := api.AgentInfo{Type: "shell", Attachable: true, State: "running"}
	head := api.AgentInfo{Type: "headless", Attachable: false, State: "running"}
	if g := plain(agentGlyph(agent)); g != glyphAgent {
		t.Errorf("AI agent glyph = %q, want %q", g, glyphAgent)
	}
	if g := plain(agentGlyph(shell)); g != glyphTerminal {
		t.Errorf("shell glyph = %q, want %q", g, glyphTerminal)
	}
	if g := plain(agentGlyph(head)); g != glyphHeadless {
		t.Errorf("headless glyph = %q, want %q", g, glyphHeadless)
	}
	// A stopped headless agent keeps the box shape (color changes, not shape).
	stopped := api.AgentInfo{Type: "headless", Attachable: false, State: "stopped"}
	if g := plain(agentGlyph(stopped)); g != glyphHeadless {
		t.Errorf("stopped headless glyph = %q, want %q (shape is stable)", g, glyphHeadless)
	}
}

func TestAgentRowDisambiguatesDuplicateLabels(t *testing.T) {
	dup := []api.AgentInfo{
		{ID: "p1", Label: "builder", Type: "claude-code"},
		{ID: "p2", Label: "builder", Type: "claude-code"}, // same label
		{ID: "p3", Label: "tests", Type: "claude-code"},   // unique label
	}
	if !labelIsAmbiguous(dup, dup[0]) {
		t.Error("dup[0] should be ambiguous (shares 'builder')")
	}
	if labelIsAmbiguous(dup, dup[2]) {
		t.Error("dup[2] 'tests' is unique, should not be ambiguous")
	}
	// The ambiguous rows carry their id so they aren't identical; the unique one doesn't.
	r0 := plain(agentRowText(dup[0], labelIsAmbiguous(dup, dup[0])))
	r1 := plain(agentRowText(dup[1], labelIsAmbiguous(dup, dup[1])))
	if r0 == r1 {
		t.Errorf("duplicate-label rows render identically: %q", r0)
	}
	if !strings.Contains(r0, "p1") || !strings.Contains(r1, "p2") {
		t.Errorf("ambiguous rows missing id handle: %q / %q", r0, r1)
	}
	if r2 := plain(agentRowText(dup[2], labelIsAmbiguous(dup, dup[2]))); strings.Contains(r2, "p3") {
		t.Errorf("unique-label row should not show its id: %q", r2)
	}
}

// TestAgentStatus locks in the normalized state vocabulary and its precedence,
// including the master-side states (exited / crash-loop / no model key) and the
// graceful degradation when State is unprobed (the island list): liveness words
// appear only once State is known; otherwise we fall back to the shim signal or
// an empty word (neutral glyph, no token).
func TestAgentStatus(t *testing.T) {
	want := func(name string, a api.AgentInfo, w string) {
		if got, _ := agentStatus(a); got != w {
			t.Errorf("%s: agentStatus = %q, want %q", name, got, w)
		}
	}
	want("orchestration error", api.AgentInfo{State: "running", Error: "boom"}, "error")
	want("exited", api.AgentInfo{State: "exited"}, "exited")
	want("needs you outranks running", api.AgentInfo{State: "running", AgentState: &api.AgentStateInfo{Latest: "waiting-for-input"}}, "needs you")
	want("missing key", api.AgentInfo{State: "running", AuthState: "missing-provider-auth"}, "no model key")
	want("crash-loop", api.AgentInfo{State: "running", Restarts: 4}, "crash-loop")
	want("stopped", api.AgentInfo{State: "stopped"}, "stopped")
	want("running task-complete is idle", api.AgentInfo{State: "running", AgentState: &api.AgentStateInfo{Latest: "task-complete"}}, "idle")
	want("running no signal is working", api.AgentInfo{State: "running"}, "working")
	// List view (State unprobed = ""): degrade to the shim signal, never claim
	// "stopped" just because liveness wasn't probed.
	want("list waiting", api.AgentInfo{AgentState: &api.AgentStateInfo{Latest: "waiting-for-input"}}, "needs you")
	want("list done", api.AgentInfo{AgentState: &api.AgentStateInfo{Latest: "task-complete"}}, "done")
	want("list unknown is blank", api.AgentInfo{}, "")
}

// TestAttachedIndicator: silent when nobody's watching, a bare dot for one
// viewer, dot+count for several.
func TestAttachedIndicator(t *testing.T) {
	if got := plain(attachedIndicator(nil)); got != "" {
		t.Errorf("no viewers should be silent, got %q", got)
	}
	if got := plain(attachedIndicator([]api.PresenceEntry{{Label: "a"}})); got != "◉" {
		t.Errorf("one viewer = %q, want ◉", got)
	}
	if got := plain(attachedIndicator([]api.PresenceEntry{{Label: "a"}, {Label: "b"}})); got != "◉2" {
		t.Errorf("two viewers = %q, want ◉2", got)
	}
}

// TestRenderListNoWrap guards the layout against the wrap bug: a full agent row
// is wider than a narrow pane, and lipgloss would wrap it onto a second line —
// shredding the tree and desyncing the selLine→viewport math. renderList must
// clip each line to the pane width instead.
func TestRenderListNoWrap(t *testing.T) {
	const w = 40 // narrower than a full agent row
	out, _ := sampleModel().renderList(w)
	for _, ln := range strings.Split(out, "\n") {
		if got := lipgloss.Width(ln); got > w {
			t.Errorf("row exceeds pane width %d (=%d), would wrap: %q", w, got, plain(ln))
		}
	}
}

// TestRenderListGlyphs renders the list and asserts each kind shows up. The
// rendered output is logged so the visual can be eyeballed with `go test -v`.
func TestRenderListGlyphs(t *testing.T) {
	m := sampleModel()
	out, _ := m.renderList(80) // wide enough that no row clips
	t.Logf("\n%s", out)        // visible under -v

	bare := plain(out)
	for _, want := range []string{
		glyphAgent + " a1",      // unlabeled AI agent → leads with its id, not type
		glyphAgent + " Backend", // labeled AI agent → leads with the label
		glyphHeadless + " a3",   // unlabeled headless → leads with its id
		"codex",                 // …with the type demoted to the muted meta column
		"up 1h",                 // a1 running with a known CreatedAt → uptime
		"◉",                     // a1 has an attached viewer → presence badge
		"needs you",             // a1's waiting-for-input normalized to the call-to-action word
		"stopped",               // a2 session is stopped
		// Tree connectors group the island's children. The secrets row now caps
		// the group (└), so add-agent branches (├) like the agent rows above it.
		"├ ", "└ + add agent", "└ " + glyphSecrets + " secrets",
		"+ new island",
	} {
		if !strings.Contains(bare, want) {
			t.Errorf("rendered list missing %q\n%s", want, bare)
		}
	}
	// The label still leads for a labeled agent — type rides along as meta, it
	// does not replace the label.
	if strings.Contains(bare, glyphAgent+" codex") {
		t.Errorf("labeled agent should lead with its label, not its type\n%s", bare)
	}
	// Unlabeled agents lead with the id, not the type name.
	if strings.Contains(bare, glyphAgent+" claude-code") {
		t.Errorf("unlabeled agent should lead with its id, not its type\n%s", bare)
	}
}

// Tree glyphs MUST measure as one cell, and lipgloss's count must match what a
// terminal draws. An emoji breaks this: it is East Asian Wide (two cells drawn)
// while the text-presentation form measures as one — the mismatch wraps the row,
// and Bubble Tea's newline-counting diff renderer then duplicates the whole view
// on every repaint. That shipped once (the VS-15 padlock in v0.8.30); this keeps
// it from shipping again.
func TestTreeGlyphsAreSingleCellAndNotEmoji(t *testing.T) {
	for name, g := range map[string]string{
		"glyphSecrets":  glyphSecrets,
		"glyphAgent":    glyphAgent,
		"glyphTerminal": glyphTerminal,
		"glyphHeadless": glyphHeadless,
	} {
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("%s (%q) lipgloss.Width=%d, want 1 — a wider glyph wraps the row and duplicates the view", name, g, w)
		}
		for _, r := range g {
			// Variation selectors and any codepoint at/above the emoji planes are
			// the danger: lipgloss and the terminal disagree on their width.
			if r == 0xFE0F || r == 0xFE0E {
				t.Errorf("%s carries a variation selector (%U) — that's the exact width trap", name, r)
			}
			if r >= 0x1F000 {
				t.Errorf("%s uses an emoji-plane rune (%U); use a text-default symbol", name, r)
			}
		}
	}
}
