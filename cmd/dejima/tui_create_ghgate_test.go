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

	// And the key must actually do something.
	out, _ := m.creatorGitHubKey(key("c"))
	m = out.(tuiModel)
	if m.creator.ghHint == hint {
		t.Error("pressing [c] changed nothing — the key is advertised and inert")
	}
	// canOpenNewWindow is false under test, so the fallback must name the exact
	// command. An operator who cannot spawn a window still has to be able to act.
	if !strings.Contains(m.creator.ghHint, "dejima github connect") {
		t.Errorf("the no-window fallback does not name the command to run:\n%s", m.creator.ghHint)
	}
	// --default, because this fires only when NO identity resolves: the one being
	// created is the one everything should follow. Omitting it is how a daemon
	// ends up holding identities and no default.
	// One constant feeds both the window args and the printed command, so this
	// single assertion covers the path a test cannot drive (the real window) as
	// well as the one it can.
	if ghConnectCmd != "dejima github connect"+ghConnectArgs {
		t.Errorf("the printed command and the window args have drifted: %q vs %q",
			ghConnectCmd, ghConnectArgs)
	}
	if !strings.Contains(ghConnectArgs, "--default") {
		t.Errorf("the connect args omit --default (%q), so the daemon can end up "+
			"holding an identity that nothing resolves to", ghConnectArgs)
	}
	if !strings.Contains(m.creator.ghHint, "--default") {
		t.Errorf("the connect does not take the default, so the daemon can end up "+
			"with an identity that nothing resolves to:\n%s", m.creator.ghHint)
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
