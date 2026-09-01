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

	// It is an OFFER now, not a warning: shown before any agent is chosen, it
	// presented an optional convenience as a missing prerequisite.
	if !strings.Contains(body, "Set up Claude for every island") {
		t.Fatalf("the offer did not render, so this test asserts nothing about its "+
			"wording:\n%s", body)
	}
	// It must advertise a key that does something, not tell the operator to go
	// and type a command — this is the dashboard.
	if !strings.Contains(body, "[L]") {
		t.Errorf("the offer names no key, so the only way to act on it is to leave "+
			"the TUI:\n%s", body)
	}
	// And it must say the credential is DAEMON-WIDE, which is the part nobody
	// could guess and the reason it is worth doing once.
	if !strings.Contains(strings.ToLower(body), "every island") {
		t.Errorf("the offer does not say it covers every island, so it reads as a "+
			"per-island chore:\n%s", body)
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
	// Still optional, and still says so: someone about to use a provider key or a
	// shell agent needs none of this.
	if !strings.Contains(strings.ToLower(body), "optional") {
		t.Errorf("the offer does not say it is optional, so it reads as a prerequisite "+
			"to people who do not need it:\n%s", body)
	}
}

// The nudge must stay silent until the check has actually run, or a fresh TUI
// warns about credentials it has not looked for yet.
func TestFirstRunCredsNudgeWaitsForTheCheck(t *testing.T) {
	m := tuiModel{setupChecked: false, claudeSeeded: false}
	body, _ := m.renderList(100)
	if strings.Contains(body, "Set up Claude for every island") {
		t.Errorf("offered credential setup before the check landed — that is a "+
			"claim about state nobody has read yet:\n%s", body)
	}
}
