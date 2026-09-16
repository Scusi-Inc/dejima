package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// The model an agent is actually running was already crossing the boundary —
// the usage hook reads the most recent assistant model id off the transcript and
// posts it with every agent.usage report, and the daemon priced the turn with it
// — and then dropped it before anything could show it. These pin the value all
// the way to the row.
//
// WHY THE MODEL REPLACES THE TYPE rather than joining it: the row has a status
// column at a fixed offset, so the meta is a width budget, not a free field.
// "claude-code" told the operator what they picked when they created the agent,
// which is the one thing they cannot have forgotten. Which model is answering
// changes underneath them.
func TestAgentRowShowsTheModelThatIsActuallyAnswering(t *testing.T) {
	withUsage := func(model string) api.IslandInfo {
		isl := island("acme", "a1")
		isl.Agents[0].Usage = &api.AgentUsage{
			TotalTokens: 1000, Source: "claude-code", Model: model,
		}
		return isl
	}

	t.Run("a reported model is what the row leads with", func(t *testing.T) {
		m := seededModel(t, withUsage("claude-opus-5"))
		out, _ := m.renderList(120)
		if !strings.Contains(plain(out), "claude-opus-5") {
			t.Errorf("the agent reported claude-opus-5 and the row does not say so:\n%s", plain(out))
		}
	})

	// Not cosmetic: a row that still said "claude-code" beside a known model would
	// be showing the one fact of the two that cannot have changed.
	t.Run("the framework id gives way to the model", func(t *testing.T) {
		m := seededModel(t, withUsage("claude-opus-5"))
		out, _ := m.renderList(120)
		if strings.Contains(plain(out), "claude-code") {
			t.Errorf("row still carries the framework id next to a known model:\n%s", plain(out))
		}
	})

	// The gap between an agent starting and its first usage report is real — the
	// hook fires on a turn boundary, so a fresh agent has no model for its first
	// turn. A blank meta there would read as a broken row.
	t.Run("no report yet falls back to the shortened framework name", func(t *testing.T) {
		m := seededModel(t, island("acme", "a1"))
		out, _ := m.renderList(120)
		if !strings.Contains(plain(out), "Claude") {
			t.Errorf("an agent with no usage report should still name its framework:\n%s", plain(out))
		}
	})
}

// The handler id is a HANDLE — the registry key, the DEJIMA_LAUNCH comparison,
// start.sh's case arms, `dejima agent add --type`. Shortening it for display is
// safe only as long as the shortening stays in the display layer, so this pins
// that agentFrameworkLabel is not a rename.
func TestShorteningTheFrameworkNameIsDisplayOnly(t *testing.T) {
	if got := agentFrameworkLabel("claude-code"); got != "Claude" {
		t.Errorf("agentFrameworkLabel(claude-code) = %q, want %q", got, "Claude")
	}
	// An unknown or non-Claude type passes through untouched rather than being
	// guessed at — inventing display names for frameworks nobody asked about is
	// how a label starts disagreeing with the thing it names.
	for _, typ := range []string{"codex", "aider", "openclaw", "some-custom-agent"} {
		if got := agentFrameworkLabel(typ); got != typ {
			t.Errorf("agentFrameworkLabel(%q) = %q — only claude-code has an agreed short name", typ, got)
		}
	}
}

// A REPORT and a CONFIGURED TARGET are different facts that happen to be the
// same shape. AgentInfo.Model is what Dejima set for a provider-key framework;
// Usage.Model is what an agent said it used. When both exist the observation
// wins — an operator's in-session model change moves one and not the other.
func TestAReportedModelBeatsAConfiguredOne(t *testing.T) {
	a := api.AgentInfo{
		ID: "a1", Type: "aider", Model: "gpt-4o-mini",
		Usage: &api.AgentUsage{Model: "claude-opus-5", Source: "claude-code"},
	}
	if got := agentReportedModel(a); got != "claude-opus-5" {
		t.Errorf("agentReportedModel = %q — what RAN must win over what was configured", got)
	}

	// With no report, the configured target is the only thing known and is better
	// than nothing; it is labelled as configured in the detail pane for that reason.
	b := api.AgentInfo{ID: "a1", Type: "aider", Model: "gpt-4o-mini"}
	if got := agentReportedModel(b); got != "gpt-4o-mini" {
		t.Errorf("agentReportedModel = %q, want the configured target when nothing was reported", got)
	}
}
