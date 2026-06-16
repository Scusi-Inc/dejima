package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
)

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

// TestRenderListGlyphs renders the list and asserts each kind shows up. The
// rendered output is logged so the visual can be eyeballed with `go test -v`.
func TestRenderListGlyphs(t *testing.T) {
	m := sampleModel()
	out, _ := m.renderList(60)
	t.Logf("\n%s", out) // visible under -v

	bare := plain(out)
	for _, want := range []string{
		glyphAgent + " a1",      // unlabeled AI agent → leads with its id, not type
		glyphAgent + " Backend", // labeled AI agent → leads with the label, not "codex"
		glyphHeadless + " a3",   // unlabeled headless → leads with its id
		"+ add agent",
		"+ new island",
	} {
		if !strings.Contains(bare, want) {
			t.Errorf("rendered list missing %q\n%s", want, bare)
		}
	}
	// The label supersedes the type in the row; the bare type shouldn't appear.
	if strings.Contains(bare, "codex") {
		t.Errorf("labeled agent should show its label, not type %q\n%s", "codex", bare)
	}
	// Unlabeled agents lead with the id now, not the type name.
	if strings.Contains(bare, glyphAgent+" claude-code") {
		t.Errorf("unlabeled agent should lead with its id, not its type\n%s", bare)
	}
}
