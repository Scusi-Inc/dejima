package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// ids extracts the agent ids from an ordered slice, for compact assertions.
func ids(agents []api.AgentInfo) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.ID
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqi(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOrderedAgentsFlat: with no lineage, order is preserved and every agent is
// top-level (depth 0).
func TestOrderedAgentsFlat(t *testing.T) {
	in := []api.AgentInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, depth := orderedAgents(in)
	if !eq(ids(got), []string{"a", "b", "c"}) {
		t.Errorf("order = %v, want [a b c]", ids(got))
	}
	if !eqi(depth, []int{0, 0, 0}) {
		t.Errorf("depth = %v, want [0 0 0]", depth)
	}
}

// TestOrderedAgentsNested: a sub-agent sits immediately after its spawner, one
// level deeper; a grandchild nests under it.
func TestOrderedAgentsNested(t *testing.T) {
	in := []api.AgentInfo{
		{ID: "p"},
		{ID: "q"},
		{ID: "child", SpawnedBy: "p"},
		{ID: "grand", SpawnedBy: "child"},
	}
	got, depth := orderedAgents(in)
	if !eq(ids(got), []string{"p", "child", "grand", "q"}) {
		t.Errorf("order = %v, want [p child grand q]", ids(got))
	}
	if !eqi(depth, []int{0, 1, 2, 0}) {
		t.Errorf("depth = %v, want [0 1 2 0]", depth)
	}
}

// TestOrderedAgentsOrphan: a SpawnedBy pointing at an agent not in this island
// is shown at the root (depth 0), never hidden.
func TestOrderedAgentsOrphan(t *testing.T) {
	in := []api.AgentInfo{{ID: "a"}, {ID: "orphan", SpawnedBy: "gone"}}
	got, depth := orderedAgents(in)
	if len(got) != 2 || !eqi(depth, []int{0, 0}) {
		t.Errorf("orphan should be top-level: order=%v depth=%v", ids(got), depth)
	}
}

// TestOrderedAgentsCycle: a SpawnedBy cycle must not hang or drop rows — every
// agent still appears exactly once.
func TestOrderedAgentsCycle(t *testing.T) {
	in := []api.AgentInfo{{ID: "a", SpawnedBy: "b"}, {ID: "b", SpawnedBy: "a"}}
	got, _ := orderedAgents(in)
	if len(got) != 2 {
		t.Fatalf("cycle should still surface both agents, got %v", ids(got))
	}
	seen := map[string]int{}
	for _, a := range got {
		seen[a.ID]++
	}
	if seen["a"] != 1 || seen["b"] != 1 {
		t.Errorf("each agent should appear once, got %v", seen)
	}
}

// TestSubAgentRowText: an ephemeral sub-agent renders its name and an ephemeral
// marker (dim/italic styling is applied; plain() strips the ANSI for the check).
func TestSubAgentRowText(t *testing.T) {
	got := plain(subAgentRowText(api.AgentInfo{ID: "w1", Label: "worker", Type: "claude-code", Ephemeral: true}))
	if !strings.Contains(got, "worker") {
		t.Errorf("row should show the agent name: %q", got)
	}
	if !strings.Contains(got, "ephemeral") {
		t.Errorf("ephemeral sub-agent should carry the ephemeral marker: %q", got)
	}
}

// TestVisibleRowsNestsSubAgents: an expanded island's sub-agent row carries
// depth>0 so the renderer indents it under its spawner.
func TestVisibleRowsNestsSubAgents(t *testing.T) {
	m := initialTUIModel(nil)
	m.islands = []api.IslandInfo{{Name: "isl", Container: "running", Agents: []api.AgentInfo{
		{ID: "boss", Attachable: true},
		{ID: "kid", SpawnedBy: "boss", Ephemeral: true, Attachable: true},
	}}}
	m.expanded["isl"] = true
	var bossDepth, kidDepth = -1, -1
	for _, r := range m.visibleRows() {
		if r.kind == rowAgent && r.agentID == "boss" {
			bossDepth = r.depth
		}
		if r.kind == rowAgent && r.agentID == "kid" {
			kidDepth = r.depth
		}
	}
	if bossDepth != 0 {
		t.Errorf("spawner row depth = %d, want 0", bossDepth)
	}
	if kidDepth != 1 {
		t.Errorf("sub-agent row depth = %d, want 1", kidDepth)
	}
}
