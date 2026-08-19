package main

import (
	"errors"
	"strings"
	"testing"
)

// confirmText flattens a rendered confirm so assertions test the WORDS rather
// than where lipgloss happened to wrap them. Without this a copy edit that only
// shifts a line break fails the test, which trains people to weaken the
// assertion until it stops meaning anything.
func confirmText(m tuiModel) string {
	return strings.Join(strings.Fields(m.renderConfirm()), " ")
}

// An agent removal the daemon's worktree guard refused has to offer a way
// through, or the operator is stuck in the TUI and has to drop to the CLI to
// finish a job the TUI started.
//
// Note what it is armed WITH: msg.agent, not the highlighted row. The reply
// arrives asynchronously and a list refresh in between can move the cursor, so
// reading the row here would point the override at whichever agent happens to
// be selected now rather than the one that was refused.
func TestGuardedAgentRemovalOffersTheOverrideForTheRightAgent(t *testing.T) {
	m := seededModel(t, island("alpha", "a1", "a2"))
	guard := errors.New(`agent "a1" has 3 uncommitted changes in its worktree on branch agent/a1 — ` +
		`removing the agent discards them permanently (its branch and commits are kept); ` +
		`commit them first, or re-run with --force to remove it anyway`)

	mm, _ := m.Update(opCompleteMsg{name: "alpha", verb: "remove-agent", err: guard, agent: "a1"})
	got := mm.(tuiModel)
	if got.confirm == nil || got.confirm.verb != "force-remove-agent" {
		t.Fatalf("a guarded removal should arm force-remove-agent, got %+v", got.confirm)
	}
	if got.confirm.agent != "a1" || got.confirm.island != "alpha" {
		t.Fatalf("override must target the refused agent a1 on alpha, got %+v", got.confirm)
	}

	prompt := confirmText(got)
	// Name the loss, and name the survivor only after it — putting "keeps its
	// branch" first is exactly how the CLI's old summary managed to be true and
	// reassuring at the same time.
	for _, want := range []string{"uncommitted", "DISCARD", "branch and commits are kept"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("force-remove prompt must mention %q; got %q", want, prompt)
		}
	}
}

// The escalation must not be cheaper than the thing it escalates. Removing an
// agent already costs a typed agent id; forcing it past a guard that has just
// PROVEN there is work to lose must cost at least as much. force-purge got this
// backwards until finding B of the same sweep fixed it — plain purge typed the
// island name while forcing it took one "y" — and repeating that here would have
// been the easy thing to do.
func TestForcedAgentRemovalStillCostsTheTypedID(t *testing.T) {
	m := seededModel(t, island("alpha", "a1"))
	m.dirtyOps = map[string]string{}

	// A single "y" must not get through.
	out, cmd := m.runConfirmed(confirmPrompt{verb: "force-remove-agent", island: "alpha", agent: "a1", answer: "y"})
	if cmd != nil {
		t.Error(`"y" must not satisfy a forced agent removal — the escalated verb cannot be the cheaper one`)
	}
	if got := out.(tuiModel).lastError; !strings.Contains(got, "a1") {
		t.Errorf("a rejected override should say what was needed; got %q", got)
	}

	// The agent id does.
	if _, cmd := m.runConfirmed(confirmPrompt{verb: "force-remove-agent", island: "alpha", agent: "a1", answer: "a1"}); cmd == nil {
		t.Error("typing the agent id should carry out the forced removal")
	}
}

// The ordinary (unforced) confirm has to name what is destroyed. "destroys its
// worktree + agent state" was the old wording, which reads as "removes a
// directory" rather than "throws away work you never committed".
func TestRemoveAgentConfirmNamesTheUncommittedLoss(t *testing.T) {
	m := seededModel(t, island("alpha", "a1"))
	m.confirm = &confirmPrompt{verb: "remove-agent", island: "alpha", agent: "a1"}
	prompt := confirmText(m)
	for _, want := range []string{"DISCARDING anything uncommitted", "branch and commits are kept"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("remove-agent confirm must mention %q; got %q", want, prompt)
		}
	}
}
