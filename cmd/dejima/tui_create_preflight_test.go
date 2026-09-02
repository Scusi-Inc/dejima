package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/reposrc"
)

// The credential is a prerequisite of the first choice in the flow and was
// checked LAST — after an island was created and an image pulled, in a terminal
// the operator had to go and read. This asks the question on the screen where
// the repo was chosen.
func TestPastingAPrivateLookingURLWithNoIdentityWarnsBeforeBuilding(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		step:               stepManual,
		manualInput:        "https://github.com/aoos/private-thing",
		ghIdentitiesLoaded: true, // asked, and the daemon holds none
	}}
	out, _ := m.creatorManualKey(key("enter"))
	c := out.(tuiModel).creator

	if c.step != stepGitHubPreflight {
		t.Fatalf("a GitHub URL with no identity went straight on to step %v — the operator "+
			"finds out after an island exists", c.step)
	}
	body := plain(renderGitHubPreflight(c.resolution.Repo))
	// It must state what is CERTAIN and what is not. We know there is no
	// identity; we do not know whether this repo needs one.
	for _, want := range []string{"No GitHub identity", "PUBLIC", "PRIVATE", "[c]", "continue anyway"} {
		if !strings.Contains(body, want) {
			t.Errorf("the preflight must mention %q:\n%s", want, body)
		}
	}
	// And it must not claim to know the answer it cannot have.
	for _, forbidden := range []string{"this will fail", "this repo is private"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("the preflight asserts %q, which it cannot know:\n%s", forbidden, body)
		}
	}
}

// It WARNS; it does not block. A public repo clones perfectly well with no
// credential, and nothing here can tell public from private without asking
// GitHub. A gate that stops the common case is a gate people learn to press
// through, which costs more than it saves.
func TestThePreflightLetsTheOperatorThrough(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		step:          stepGitHubPreflight,
		preflightName: "private-thing",
		resolution:    reposrc.Resolution{Repo: "https://github.com/aoos/private-thing"},
	}}
	out, _ := m.creatorGitHubPreflightKey(key("enter"))
	c := out.(tuiModel).creator
	if c == nil || c.step == stepGitHubPreflight {
		t.Error("[enter] did not continue — the warning blocks a public clone that would have worked")
	}
}

// [c] goes straight into the in-pane sign-in. Not a printed command, not a
// spawned window: the operator asked for GitHub to be handled in the TUI, and
// this is the screen where they discover they need it.
func TestThePreflightConnectsInThePane(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		step:       stepGitHubPreflight,
		resolution: reposrc.Resolution{Repo: "https://github.com/aoos/private-thing"},
	}}
	out, cmd := m.creatorGitHubPreflightKey(key("c"))
	mm := out.(tuiModel)
	if mm.creator != nil {
		t.Error("[c] left the creator open behind the sign-in")
	}
	if mm.github == nil || mm.github.connect == nil {
		t.Fatal("[c] did not start a sign-in in the GitHub pane")
	}
	if cmd == nil {
		t.Error("[c] issued no command, so nothing asks the daemon for a code")
	}
}

// THE FALSE-WARNING CASES, which matter more than the true one: a warning that
// fires when it should not is how the real one gets ignored.
func TestThePreflightStaysSilentWhenItShould(t *testing.T) {
	withDefault := []githubid.Meta{{Name: "aoos", Default: true}}
	noDefault := []githubid.Meta{{Name: "aoos"}}

	cases := []struct {
		name   string
		repo   string
		ids    []githubid.Meta
		loaded bool
		want   bool
	}{
		{"github url, no identity at all", "https://github.com/a/b", nil, true, true},
		{"github url, identities but none default", "https://github.com/a/b", noDefault, true, true},
		{"github url, a default exists", "https://github.com/a/b", withDefault, true, false},
		{"ssh github url, no default", "git@github.com:a/b.git", nil, true, true},
		{"not github at all", "https://gitlab.com/a/b", nil, true, false},
		{"a local path", "/home/me/code/thing", nil, true, false},
		{"empty", "", nil, true, false},
		// The one that matters most: we never asked. Not knowing is not the same
		// as knowing there is none.
		{"identity list never loaded", "https://github.com/a/b", nil, false, false},
	}
	for _, tc := range cases {
		if got := creatorPreflightGitHub(tc.repo, tc.ids, tc.loaded); got != tc.want {
			t.Errorf("%s: warn=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// A lookup that FAILED must not warn either — and must not strand the operator
// on a screen about a credential we could not check. It goes through.
func TestAFailedIdentityLookupDoesNotWarnOrStall(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		step:          stepGitHubPreflight,
		ghLoading:     true,
		preflightName: "thing",
		resolution:    reposrc.Resolution{Repo: "https://github.com/a/b"},
	}}
	out, _ := m.onGhIdentities(ghIdentitiesMsg{err: errors.New("daemon unreachable")})
	c := out.(tuiModel).creator
	if c.step == stepGitHubPreflight {
		t.Error("a failed identity lookup left the operator on a warning about a " +
			"credential nothing could see")
	}
}

// And when the daemon DOES report a default, the preflight resolves itself
// without the operator seeing anything at all — the check is silent when the
// answer is fine.
func TestThePreferredPathIsSilent(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		step:          stepGitHubPreflight,
		ghLoading:     true,
		preflightName: "thing",
		resolution:    reposrc.Resolution{Repo: "https://github.com/a/b"},
	}}
	out, _ := m.onGhIdentities(ghIdentitiesMsg{identities: []githubid.Meta{{Name: "aoos", Default: true}}})
	c := out.(tuiModel).creator
	if c.step == stepGitHubPreflight {
		t.Error("a working default still showed the warning screen")
	}
}
