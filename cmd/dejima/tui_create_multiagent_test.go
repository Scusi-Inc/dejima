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

	c.agents = []api.AgentSpecRequest{{Type: "shell"}}
	req := c.buildRequest()
	if req.Agent != "shell" || req.Cmd != "" || req.Agents != nil {
		t.Fatalf("single-agent request = %+v; want scalar shell, nil Agents", req)
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

	// Pick the primary (shell leads the picker) → lands on the roster.
	m = feedCreator(m, "enter")
	if c.step != stepAgents || len(c.agents) != 1 || c.agents[0].Type != "shell" {
		t.Fatalf("after primary: step=%v agents=%+v", c.step, c.agents)
	}

	// Add an extra agent: 'a' → picker, pick claude-code (down,enter) → roster.
	m = feedCreator(m, "a", "down", "enter")
	if c.step != stepAgents || len(c.agents) != 2 || c.agents[1].Type != "claude-code" {
		t.Fatalf("after add: step=%v agents=%+v", c.step, c.agents)
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

	// Continue → name step.
	m = feedCreator(m, "enter")
	if c.step != stepName {
		t.Fatalf("continue: step=%v, want stepName", c.step)
	}

	// esc from name → back to the roster.
	feedCreator(m, "esc")
	if c.step != stepAgents {
		t.Fatalf("esc from name: step=%v, want stepAgents", c.step)
	}
}

// backing out of an extra-agent pick discards it and returns to the roster.
func TestExtraPickBackDiscards(t *testing.T) {
	c := &creatorModel{step: stepAgent, picker: newAgentPicker(), resolution: reposrc.Resolution{Repo: "r"}}
	m := tuiModel{creator: c}
	m = feedCreator(m, "enter") // primary
	m = feedCreator(m, "a")     // start adding extra
	if c.step != stepAgent || !c.pickingExtra {
		t.Fatalf("not in extra-pick: step=%v pickingExtra=%v", c.step, c.pickingExtra)
	}
	feedCreator(m, "esc") // back out of the picker
	if c.step != stepAgents || len(c.agents) != 1 || c.pickingExtra {
		t.Fatalf("after back: step=%v agents=%+v pickingExtra=%v", c.step, c.agents, c.pickingExtra)
	}
}
