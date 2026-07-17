package main

import "testing"

// TestIslandCreatedOpensPrimaryAgent: after creating a (multi-agent) island, the
// auto-open targets the PRIMARY agent by id — not the bare island, which would
// dump the operator onto the `connect` "Attach which? [1]" picker (mbx 286).
// canOpenNewWindow is forced false so the in-terminal fallback runs (the
// new-window path only execs, which isn't drivable in-container); both paths
// carry the same agent id.
func TestIslandCreatedOpensPrimaryAgent(t *testing.T) {
	orig := canOpenNewWindow
	canOpenNewWindow = func() bool { return false }
	t.Cleanup(func() { canOpenNewWindow = orig })

	m := tuiModel{creator: &creatorModel{}, expanded: map[string]bool{}}
	updated, _ := m.Update(islandCreatedMsg{name: "isl", agentID: "ag-1", agentLabel: "claude"})
	got := updated.(tuiModel)
	if got.connectTo != "isl" || got.connectAgent != "ag-1" {
		t.Errorf("post-create should connect into the primary agent, got connectTo=%q connectAgent=%q",
			got.connectTo, got.connectAgent)
	}
}
