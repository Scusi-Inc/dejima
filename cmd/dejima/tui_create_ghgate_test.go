package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func ghGateModel(role string) tuiModel {
	m := tuiModel{creator: &creatorModel{step: stepGitHub, callerRole: role}}
	out, _ := m.onGhIdentities(ghIdentitiesMsg{identities: nil})
	return out.(tuiModel)
}

// An owner with no GitHub identity must be able to CONNECT from here, not be
// handed instructions for another terminal.
//
// The screen already guided — in prose. It said to go run the push-credentials
// command somewhere else and come back, which is the settings pane's job, and
// the settings pane has a key for it. Reported from a fresh Windows install as
// "no github connected (should be guided)": the guidance existed and was
// reading material, so the operator read instructions instead of connecting.
func TestCreatorGitHubGateOffersAGuidedConnect(t *testing.T) {
	m := ghGateModel("owner")
	hint := m.creator.ghHint
	if hint == "" {
		t.Fatal("no hint rendered for an owner with zero identities")
	}
	if !strings.Contains(hint, "[c]") {
		t.Errorf("the gate does not offer a key to connect — an owner reads a "+
			"paragraph and leaves:\n%s", hint)
	}

	// And the key must actually do something — which is now HANDING OFF TO THE
	// GITHUB PANE, where the device flow runs in-process.
	//
	// It used to spawn `dejima github connect` in a terminal window and print the
	// command as a fallback. Both halves are gone deliberately. The window died
	// instantly on Windows while the screen reported that it had opened, and the
	// printed command is a CLI instruction pushed at someone using the TUI —
	// which the operator asked us to stop doing, in those words.
	out, cmd := m.creatorGitHubKey(key("c"))
	m = out.(tuiModel)
	if m.creator != nil {
		t.Error("[c] left the creator open — the create it was mid-way through has " +
			"already been refused, and resuming a wizard into a stale answer is worse " +
			"than starting again")
	}
	if m.github == nil || m.github.connect == nil {
		t.Fatal("[c] did not open the GitHub pane with a sign-in running — the key is " +
			"advertised and inert")
	}
	if cmd == nil {
		t.Error("[c] issued no command, so nothing ever asks the daemon for a code")
	}
	if !m.github.connect.makeDefault {
		t.Error("the connect does not take the default: this gate fires only when NO " +
			"identity resolves, so the one being created is the one everything should follow")
	}
	// And it must not have gone back to pushing a shell command at anyone.
	if body := m.renderGithubView(); strings.Contains(body, "dejima github connect") {
		t.Errorf("the pane still prints a CLI command at a TUI user:\n%s", body)
	}
}

// [r] must re-ask the daemon rather than assume the connect worked.
func TestCreatorGitHubGateReloads(t *testing.T) {
	m := ghGateModel("owner")
	out, cmd := m.creatorGitHubKey(key("r"))
	m = out.(tuiModel)
	if cmd == nil {
		t.Error("[r] issued no reload — the operator has connected and has no way " +
			"to tell this screen about it")
	}
	if !m.creator.ghLoading {
		t.Error("[r] did not enter the loading state, so a slow daemon looks like a " +
			"key that did nothing")
	}
}

// A teammate cannot add an identity (the route is owner-only), so offering them
// a connect key would be a button that always fails. They get the ask-the-owner
// path instead — and must NOT be told to press [c].
func TestCreatorGitHubGateDoesNotOfferConnectToTeammates(t *testing.T) {
	for _, role := range []string{"operator", "viewer"} {
		m := ghGateModel(role)
		if strings.Contains(m.creator.ghHint, "[c]") {
			t.Errorf("role %q is offered a connect key, but adding an identity is "+
				"owner-only — the key would always fail:\n%s", role, m.creator.ghHint)
		}
		if !strings.Contains(strings.ToLower(m.creator.ghHint), "owner") {
			t.Errorf("role %q is not told who can fix this:\n%s", role, m.creator.ghHint)
		}
	}
}

var _ = api.GitHubIdentityView{}
