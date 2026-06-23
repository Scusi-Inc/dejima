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
				CreatedAt:  time.Now().Add(-90 * time.Minute),
				AgentState: &api.AgentStateInfo{Latest: "waiting-for-input", UpdatedAt: time.Unix(0, 0)}},
			{ID: "a2", Type: "codex", Label: "Backend", Attachable: true, State: "stopped",
				CreatedAt: time.Now().Add(-90 * time.Minute)},
			{ID: "a3", Type: "headless", Attachable: false, State: "running", Error: "worktree add failed"},
		},
	}}
	return m
}

// TestAgentGlyphKind locks in shape = identity: terminal agents get the prompt
// glyph, headless agents get the box — independent of state.
func TestAgentGlyphKind(t *testing.T) {
	term := api.AgentInfo{Type: "claude-code", Attachable: true, State: "running"}
	head := api.AgentInfo{Type: "headless", Attachable: false, State: "running"}
	if g := plain(agentGlyph(term)); g != glyphTerminal {
		t.Errorf("terminal glyph = %q, want %q", g, glyphTerminal)
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

// TestAgentStatus locks in the normalized state vocabulary and its precedence:
// an orchestration error or an error signal outranks everything; a pending
// "needs you" outranks liveness; a dead session reads "stopped"; a live one is
// "idle" only once it reports task-complete, else "working".
func TestAgentStatus(t *testing.T) {
	cases := []struct {
		name string
		a    api.AgentInfo
		want string
	}{
		{"orchestration error", api.AgentInfo{State: "running", Error: "worktree add failed"}, "error"},
		{"error signal", api.AgentInfo{State: "running", AgentState: &api.AgentStateInfo{Latest: "error"}}, "error"},
		{"needs you outranks running", api.AgentInfo{State: "running", AgentState: &api.AgentStateInfo{Latest: "waiting-for-input"}}, "needs you"},
		{"stopped session", api.AgentInfo{State: "stopped"}, "stopped"},
		{"empty session is not running", api.AgentInfo{State: ""}, "stopped"},
		{"task-complete is idle", api.AgentInfo{State: "running", AgentState: &api.AgentStateInfo{Latest: "task-complete"}}, "idle"},
		{"running, no signal, is working", api.AgentInfo{State: "running"}, "working"},
	}
	for _, c := range cases {
		if got, _ := agentStatus(c.a); got != c.want {
			t.Errorf("%s: agentStatus = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRenderListGlyphs renders the list and asserts each kind shows up. The
// rendered output is logged so the visual can be eyeballed with `go test -v`.
func TestRenderListGlyphs(t *testing.T) {
	m := sampleModel()
	out := m.renderList(60)
	t.Logf("\n%s", out) // visible under -v

	bare := plain(out)
	for _, want := range []string{
		glyphTerminal + " claude-code", // unlabeled terminal → leads with type
		glyphTerminal + " Backend",     // labeled terminal → leads with the label
		"Backend         codex",        // …and shows its type in the aligned type column
		glyphHeadless + " headless",    // headless box, unlabeled
		"·a1", "·a2", "·a3",            // id rides along as a muted handle
		"·a1  up 1h",     // running agent shows compact uptime
		"needs you",      // a1's waiting-for-input normalized to the call-to-action word
		"·a2", "stopped", // a2 is a stopped session
		"+ add agent",
		"+ new island",
	} {
		if !strings.Contains(bare, want) {
			t.Errorf("rendered list missing %q\n%s", want, bare)
		}
	}
	// A label-less agent's name already is its type — the type column stays
	// blank rather than repeating it, so "claude-code" appears exactly once.
	if n := strings.Count(bare, "claude-code"); n != 1 {
		t.Errorf("unlabeled agent should not repeat its type: %q appears %d×, want 1\n%s", "claude-code", n, bare)
	}
	// Uptime shows only for a running agent with a known CreatedAt: a1 qualifies;
	// a2 is stopped and a3 has a zero CreatedAt, so neither should show "up".
	if n := strings.Count(bare, " up "); n != 1 {
		t.Errorf("uptime should show once (running + known CreatedAt): %q appears %d×, want 1\n%s", " up ", n, bare)
	}
}
