package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/charmbracelet/lipgloss"
)

// TestAgentRowStatusAligned: the state word starts at the same visible column
// across rows with different-width meta (type + uptime), and lands at
// agentStatusCol.
func TestAgentRowStatusAligned(t *testing.T) {
	now := time.Now()
	mk := func(id, typ string, age time.Duration) api.AgentInfo {
		return api.AgentInfo{ID: id, Type: typ, State: "running", CreatedAt: now.Add(-age),
			AgentState: &api.AgentStateInfo{Latest: ""}} // → "working"
	}
	rows := []api.AgentInfo{
		mk("a1", "codex", 5*time.Hour),          // short meta
		mk("a2", "claude-code", 40*time.Minute), // long meta
		mk("a3", "headless", 2*time.Hour),
	}
	col := -1
	for _, a := range rows {
		bare := plain(agentRowText(a, false))
		i := strings.Index(bare, "working")
		if i < 0 {
			t.Fatalf("no status word in row: %q", bare)
		}
		// Visible column (not byte offset — the glyph is multi-byte).
		vis := lipgloss.Width(bare[:i])
		if col == -1 {
			col = vis
		} else if vis != col {
			t.Errorf("status not aligned: visible col %d, want %d (row %q)", vis, col, bare)
		}
	}
	if col != agentStatusCol {
		t.Errorf("aligned status col = %d, want agentStatusCol=%d", col, agentStatusCol)
	}
}

// TestAgentRowOverflowGap: a row too wide for the column doesn't collide — the
// status still trails with at least a 2-space gap (the "within reason" case).
func TestAgentRowOverflowGap(t *testing.T) {
	// A disambiguated name + a long type pushes the meta past agentStatusCol.
	a := api.AgentInfo{ID: "a2", Type: "claude-code", State: "running",
		CreatedAt: time.Now().Add(-40 * time.Minute), AgentState: &api.AgentStateInfo{Latest: ""}}
	row := plain(agentRowText(a, true)) // ambiguous → wide name
	i := strings.Index(row, "working")
	if i < 0 {
		t.Fatalf("no status word: %q", row)
	}
	// Whatever the width, there must be a gap before the status (no collision).
	if !strings.Contains(row, "  working") {
		t.Errorf("status should keep a ≥2-space gap, got %q", row)
	}
}
