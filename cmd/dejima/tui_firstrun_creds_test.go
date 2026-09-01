package main

import (
	"strings"
	"testing"
)

// The first-run credentials nudge must not claim the credential push authenticates
// agents it does not.
//
// It said "so claude-code/codex agents start authenticated". That is false:
// the push sends a CLAUDE session — agentcreds.LoadClaude is the only thing
// the package loads — and codex is OpenAI. A Claude session does not
// authenticate it.
//
// A wrong sentence about credentials, on the first screen a new operator sees,
// is worse than no sentence. Someone who ran auth push and then added a codex
// agent would believe it was set up and meet the failure at first attach, with
// the one surface that mentioned it having told them otherwise.
//
// → reported by d4, who found it while reviewing a different piece of work and
// flagged it rather than claiming it.
func TestFirstRunCredsNudgeDoesNotOverclaim(t *testing.T) {
	m := tuiModel{setupChecked: true, claudeSeeded: false}
	body, _ := m.renderList(100)

	if !strings.Contains(body, "no Claude credentials yet") {
		t.Fatalf("the nudge did not render, so this test asserts nothing about its "+
			"wording:\n%s", body)
	}
	// The specific false claim, and the shape of it: naming codex anywhere in a
	// sentence about what auth push authenticates is the error.
	if strings.Contains(strings.ToLower(body), "codex") {
		t.Errorf("the first-run credentials nudge mentions codex. The credential push "+
			"pushes a Claude session (agentcreds.LoadClaude); codex is OpenAI and signs "+
			"in on its own. Telling an operator otherwise on the first screen is a "+
			"credentials claim that is simply untrue:\n%s", body)
	}
	// And it must still say what auth push DOES do, or removing the false half
	// would leave a warning with no action.
	if !strings.Contains(body, "claude-code") {
		t.Errorf("the nudge no longer says which agent type it helps, so the operator "+
			"has a warning and no action:\n%s", body)
	}
}

// The nudge must stay silent until the check has actually run, or a fresh TUI
// warns about credentials it has not looked for yet.
func TestFirstRunCredsNudgeWaitsForTheCheck(t *testing.T) {
	m := tuiModel{setupChecked: false, claudeSeeded: false}
	body, _ := m.renderList(100)
	if strings.Contains(body, "no Claude credentials yet") {
		t.Errorf("warned about missing credentials before the check landed — that is a "+
			"claim about state nobody has read yet:\n%s", body)
	}
}
