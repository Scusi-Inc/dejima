package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/link"
)

// A confirmation's cost should track what the action destroys, not which code
// path added it. Two verbs got this backwards for the same reason: the
// escalated variant was written later, next to its milder parent, and inherited
// the cheaper gate by proximity.
//
// Neither gate had a test before this file. Changing them broke nothing, which
// is how they drifted in the first place — the copy was asserted, the gate was
// not.

// force-purge is plain purge PLUS overriding the unpushed-work guard, so it
// cannot be the cheaper of the two. It is offered at the one moment the daemon
// has PROVEN there is work to lose, and the operator has already typed the
// island name once to get here — which makes asking again both the cheapest
// possible confirmation and the best-justified one in the app.
func TestForcePurgeCostsAtLeastAsMuchAsPurge(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.dirtyOps = map[string]string{}

	if _, cmd := m.runConfirmed(confirmPrompt{verb: "force-purge", island: "alpha", answer: "y"}); cmd != nil {
		t.Error(`"y" must not force-purge — the escalated verb cannot be cheaper than the one it escalates`)
	}
	out, cmd := m.runConfirmed(confirmPrompt{verb: "force-purge", island: "alpha", answer: "alpha"})
	if cmd == nil {
		t.Fatal("typing the island name should carry out the forced purge")
	}
	if got := out.(tuiModel).dirtyOps["alpha"]; got != "purging" {
		t.Errorf("forced purge should mark the island purging, got %q", got)
	}

	// And the prompt has to ASK for the name, or the operator sits typing y.
	m.confirm = &confirmPrompt{verb: "force-purge", island: "alpha"}
	if got := confirmText(m); !strings.Contains(got, "the island name (alpha)") {
		t.Errorf("force-purge prompt must name what to type; got %q", got)
	}
}

// approve-action executes something on ANOTHER island — the widest blast radius
// in the app. Its comment said "never rubber-stamp a destructive action" while
// a single keystroke satisfied it, which is a containment claim in a source
// file that the code did not support. remove-secret, far less consequential,
// already asks for a typed name.
func TestApproveDestructiveActionNeedsTheActionID(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.dirtyOps = map[string]string{}
	m.pendingActions = []link.ActionRequest{{
		ID: "act_7f3", From: "wildfire", FromAgent: "w1", FromLabel: "builder",
		To: "harbor", ToAgent: "h1", ToLabel: "deployer",
		Topic: "deploy", Action: "rollback", Tier: link.TierDestructive,
		Params: `{"release":"v2"}`,
	}}

	if _, cmd := m.runConfirmed(confirmPrompt{verb: "approve-action", agent: "act_7f3", answer: "y"}); cmd != nil {
		t.Error(`"y" must not approve a destructive cross-island action`)
	}
	if _, cmd := m.runConfirmed(confirmPrompt{verb: "approve-action", agent: "act_7f3", answer: "act_7f3"}); cmd == nil {
		t.Error("typing the action id should approve it")
	}
}

// The confirm REPLACES the approvals pane rather than overlaying it, so
// whatever the operator was reading is off-screen by the time they answer. An
// approval prompt that names the action only by opaque id is asking them to
// approve something they can no longer see — and the pane's own code says
// "never approve blind".
func TestApproveActionConfirmCarriesTheDetail(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.pendingActions = []link.ActionRequest{{
		ID: "act_7f3", From: "wildfire", FromAgent: "w1", FromLabel: "builder",
		To: "harbor", ToAgent: "h1", ToLabel: "deployer",
		Topic: "deploy", Action: "rollback", Tier: link.TierDestructive,
		Params: `{"release":"v2"}`,
	}}
	m.confirm = &confirmPrompt{verb: "approve-action", agent: "act_7f3"}

	got := confirmText(m)
	for _, want := range []string{"rollback", "wildfire", "builder", "harbor", "deployer", "deploy", `{"release":"v2"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("approve confirm must show %q — the pane it replaced is no longer readable; got %q", want, got)
		}
	}
}

// The queue is in-memory and TTL-expires, so an action can vanish between
// arming the confirm and rendering it. Rendering a confident-looking prompt
// with the detail silently missing is the failure this whole sweep is about:
// say that the thing can't be shown rather than showing nothing and looking
// complete.
func TestApproveActionConfirmSaysWhenTheActionIsGone(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.pendingActions = nil // expired out from under the confirm
	m.confirm = &confirmPrompt{verb: "approve-action", agent: "act_gone"}

	got := confirmText(m)
	if !strings.Contains(got, "no longer in the pending queue") {
		t.Errorf("a vanished action must be reported, not rendered as a blank detail; got %q", got)
	}
	if !strings.Contains(got, "Cancel") {
		t.Errorf("and it should say what to do instead; got %q", got)
	}
}
