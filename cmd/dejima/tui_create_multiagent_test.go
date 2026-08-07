package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/reposrc"
)

// drive the creator's key handler from a given step.
func feedCreator(m tuiModel, keys ...string) tuiModel {
	for _, k := range keys {
		next, _ := m.creatorKey(key(k))
		m = next.(tuiModel)
	}
	return m
}

func TestBuildRequestSingleVsMulti(t *testing.T) {
	c := &creatorModel{nameInput: "isle", resolution: reposrc.Resolution{Repo: "git@x:y.git"}}

	// The roster is sent even for one agent — it is the only field that can
	// carry a Label. The scalar Agent/Cmd stay populated for older daemons.
	c.agents = []api.AgentSpecRequest{{Type: "shell"}}
	req := c.buildRequest()
	if req.Agent != "shell" || req.Cmd != "" {
		t.Fatalf("single-agent scalars = %+v; want scalar shell", req)
	}
	if len(req.Agents) != 1 || req.Agents[0].Type != "shell" {
		t.Fatalf("single-agent Agents = %+v; want the roster sent so a label can ride along", req.Agents)
	}

	c.agents = []api.AgentSpecRequest{
		{Type: "claude-code"},
		{Type: "headless", Cmd: "python loop.py"},
	}
	req = c.buildRequest()
	if req.Agent != "claude-code" {
		t.Errorf("primary scalar = %q, want claude-code", req.Agent)
	}
	if len(req.Agents) != 2 || req.Agents[1].Cmd != "python loop.py" {
		t.Fatalf("multi-agent Agents = %+v, want 2 incl headless cmd", req.Agents)
	}
}

// Full roster flow: pick the primary, add an extra, remove it, add another,
// then continue — asserting the step machine and roster contents track.
func TestRosterFlow(t *testing.T) {
	c := &creatorModel{step: stepAgent, picker: newAgentPicker(), resolution: reposrc.Resolution{Repo: "r"}}
	m := tuiModel{creator: c}

	// Pick the primary (shell leads the picker) → naming step → roster. The
	// agent is only committed once its name is confirmed, so a blank Enter here
	// is the "don't care" path that keeps the generated id.
	m = feedCreator(m, "enter")
	if c.step != stepAgentName {
		t.Fatalf("after primary pick: step=%v, want stepAgentName", c.step)
	}
	m = feedCreator(m, "enter")
	if c.step != stepAgents || len(c.agents) != 1 || c.agents[0].Type != "shell" {
		t.Fatalf("after primary: step=%v agents=%+v", c.step, c.agents)
	}
	if c.agents[0].Label != "" {
		t.Errorf("blank name should leave Label empty, got %q", c.agents[0].Label)
	}

	// Add an extra agent: 'a' → picker, pick claude-code (down,enter), then type
	// a name for it — naming at creation time is the point of the extra step.
	m = feedCreator(m, "a", "down", "enter")
	m = feedCreator(m, "a", "p", "i", "enter")
	if c.step != stepAgents || len(c.agents) != 2 || c.agents[1].Type != "claude-code" {
		t.Fatalf("after add: step=%v agents=%+v", c.step, c.agents)
	}
	if c.agents[1].Label != "api" {
		t.Errorf("agent label = %q, want \"api\"", c.agents[1].Label)
	}

	// Remove the last (extra) — primary stays.
	m = feedCreator(m, "d")
	if len(c.agents) != 1 || c.agents[0].Type != "shell" {
		t.Fatalf("after remove: agents=%+v", c.agents)
	}

	// 'd' again must not drop the primary.
	m = feedCreator(m, "d")
	if len(c.agents) != 1 {
		t.Fatalf("primary dropped: agents=%+v", c.agents)
	}

	// Continue → creates. The island name is asked for BEFORE the agent now, so
	// the roster is the last step rather than a detour on the way to naming.
	feedCreator(m, "enter") // c (a pointer) captures the state change; the returned model is unused here
	if c.step != stepCreate {
		t.Fatalf("continue: step=%v, want stepCreate", c.step)
	}
}

// backing out of an extra-agent pick discards it and returns to the roster.
func TestExtraPickBackDiscards(t *testing.T) {
	c := &creatorModel{step: stepAgent, picker: newAgentPicker(), resolution: reposrc.Resolution{Repo: "r"}}
	m := tuiModel{creator: c}
	m = feedCreator(m, "enter", "enter") // primary: pick, then accept the default name
	m = feedCreator(m, "a")              // start adding extra
	if c.step != stepAgent || !c.pickingExtra {
		t.Fatalf("not in extra-pick: step=%v pickingExtra=%v", c.step, c.pickingExtra)
	}
	feedCreator(m, "esc") // back out of the picker
	if c.step != stepAgents || len(c.agents) != 1 || c.pickingExtra {
		t.Fatalf("after back: step=%v agents=%+v pickingExtra=%v", c.step, c.agents, c.pickingExtra)
	}
}

// A SINGLE named agent must carry its label to the daemon. buildRequest used to
// send the Agents roster only when len>1, falling back to the scalar
// Agent/Cmd fields — which have no Label — so the name typed at creation was
// silently dropped and the island came up labelled by type ("claude"). One
// agent is the common case, so this was the default experience.
func TestSingleAgentKeepsItsLabel(t *testing.T) {
	c := &creatorModel{
		nameInput:  "wildfire",
		resolution: reposrc.Resolution{Repo: "https://github.com/aoos/wildfire.git"},
		agents:     []api.AgentSpecRequest{{Type: "claude-code", Label: "ridgeops"}},
	}
	req := c.buildRequest()

	if len(req.Agents) != 1 {
		t.Fatalf("Agents = %+v, want the roster sent even for one agent", req.Agents)
	}
	if req.Agents[0].Label != "ridgeops" {
		t.Errorf("label = %q, want %q — the name typed at creation was dropped", req.Agents[0].Label, "ridgeops")
	}
	// The scalar fields stay populated for back-compat with older daemons.
	if req.Agent != "claude-code" {
		t.Errorf("scalar Agent = %q, want claude-code", req.Agent)
	}
}
