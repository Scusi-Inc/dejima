package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/githubid"
)

// newRepoModel is a creator parked on the repo list for a chosen identity — the
// state the operator is in when they decide the repo they want does not exist.
func newRepoModel() tuiModel {
	return tuiModel{creator: &creatorModel{
		step:    stepGitHub,
		ghPhase: ghPickRepo,
		ghIdentities: []githubid.Meta{
			{Name: "work", Login: "octocat", Host: "github.com"},
			{Name: "personal", Login: "monalisa", Host: "github.com"},
		},
		ghIdentity: "work",
		ghRepos:    []githubid.Repo{{NameWithOwner: "octocat/existing", URL: "https://x/e.git"}},
	}}
}

// The new-repo branch must be reachable from the repo list.
func TestRepoListOffersCreatingANewRepo(t *testing.T) {
	m := newRepoModel()
	var b strings.Builder
	m.creator.viewGitHub(&b)
	if !strings.Contains(b.String(), "[n]") {
		t.Fatalf("the repo list does not advertise creating a new repo:\n%s", b.String())
	}
	out, _ := m.creatorGitHubKey(key("n"))
	if got := out.(tuiModel).creator.step; got != stepNewRepo {
		t.Fatalf("[n] left the wizard on step %v, want stepNewRepo", got)
	}
}

// An identity with NO repos must offer the same thing.
//
// It is the likeliest place to want a new repo and it used to be a dead end that
// said "no repositories found. [esc] back" — a correct sentence that leaves the
// operator with nothing to do.
func TestAnIdentityWithNoReposCanStillCreateOne(t *testing.T) {
	m := newRepoModel()
	m.creator.ghRepos = nil
	var b strings.Builder
	m.creator.viewGitHub(&b)
	if !strings.Contains(b.String(), "[n]") {
		t.Fatalf("an empty repo list is still a dead end:\n%s", b.String())
	}
	out, _ := m.creatorGitHubKey(key("enter"))
	if got := out.(tuiModel).creator.step; got != stepNewRepo {
		t.Fatalf("enter on an empty list went to %v, want stepNewRepo", got)
	}
}

// The default visibility must be private.
//
// This is the one default in the wizard whose wrong value is a disclosure rather
// than an inconvenience. An operator who types a name and hits enter twice
// should end up with a private repo.
func TestNewRepoDefaultsToPrivate(t *testing.T) {
	m := newRepoModel()
	out, _ := m.creatorGitHubKey(key("n"))
	c := out.(tuiModel).creator
	if !c.newRepoPrivate {
		t.Fatal("a new repo defaults to PUBLIC — an operator hitting enter through " +
			"this wizard publishes their code")
	}
}

// The confirm screen must name the account and the visibility.
//
// Both are invisible after the fact and expensive to get wrong: a repo on the
// wrong account is a nuisance, a private repo made public is a disclosure. This
// is the screen the operator reads at the moment they commit, so the facts have
// to be ON it, not one screen back.
func TestConfirmScreenNamesAccountAndVisibility(t *testing.T) {
	m := newRepoModel()
	out, _ := m.creatorGitHubKey(key("n"))
	m = out.(tuiModel)
	m.creator.newRepoName = "newthing"
	out, _ = m.creatorNewRepoKey(key("enter"))
	m = out.(tuiModel)
	if m.creator.step != stepNewRepoConfirm {
		t.Fatalf("enter on the name field went to %v, want stepNewRepoConfirm", m.creator.step)
	}

	var b strings.Builder
	m.creator.viewNewRepoConfirm(&b)
	got := b.String()
	for _, want := range []string{"octocat", "github.com", "octocat/newthing", "private"} {
		if !strings.Contains(got, want) {
			t.Errorf("the confirm screen never mentions %q:\n%s", want, got)
		}
	}

	// Toggling must be visible, and PUBLIC must read louder than private — it is
	// the irreversible half.
	out, _ = m.creatorNewRepoConfirmKey(key("p"))
	m = out.(tuiModel)
	if m.creator.newRepoPrivate {
		t.Fatal("[p] did not toggle visibility")
	}
	b.Reset()
	m.creator.viewNewRepoConfirm(&b)
	if !strings.Contains(b.String(), "PUBLIC") {
		t.Errorf("a public repo is not called out on the confirm screen:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "anyone on the internet") {
		t.Errorf("the confirm screen does not say what public MEANS:\n%s", b.String())
	}
}

// An invalid name must be caught on the name screen, not after confirming.
func TestNewRepoRejectsABadNameBeforeConfirm(t *testing.T) {
	m := newRepoModel()
	out, _ := m.creatorGitHubKey(key("n"))
	m = out.(tuiModel)
	m.creator.newRepoName = "not a valid name"
	out, _ = m.creatorNewRepoKey(key("enter"))
	m = out.(tuiModel)
	if m.creator.step == stepNewRepoConfirm {
		t.Fatal("an invalid name reached the confirm screen, so the operator confirms " +
			"a create that cannot succeed")
	}
	if m.creator.err == "" {
		t.Error("the name was rejected with no explanation")
	}
}

// esc must be safe on the confirm screen, and must SAY so.
//
// It is the last screen before something exists on someone's GitHub account; an
// operator who is unsure needs to know that backing out costs nothing.
func TestConfirmScreenSaysNothingHasBeenCreatedYet(t *testing.T) {
	m := newRepoModel()
	out, _ := m.creatorGitHubKey(key("n"))
	m = out.(tuiModel)
	m.creator.newRepoName = "app"
	out, _ = m.creatorNewRepoKey(key("enter"))
	m = out.(tuiModel)

	var b strings.Builder
	m.creator.viewNewRepoConfirm(&b)
	if !strings.Contains(b.String(), "nothing has been created") {
		t.Errorf("the confirm screen does not tell the operator that backing out is "+
			"free:\n%s", b.String())
	}
	out, _ = m.creatorNewRepoConfirmKey(key("esc"))
	if got := out.(tuiModel).creator.step; got != stepNewRepo {
		t.Fatalf("esc from confirm went to %v, want back to the name screen", got)
	}
}

// A create in flight must swallow input.
//
// Enter is the create key. Without this, a second enter — an impatient operator
// on a slow link — fires a second POST and they end up with app and app-1, one
// of which nothing is using.
func TestConfirmIgnoresInputWhileCreating(t *testing.T) {
	m := newRepoModel()
	m.creator.step = stepNewRepoConfirm
	m.creator.newRepoName = "app"
	m.creator.newRepoBusy = true

	out, cmd := m.creatorNewRepoConfirmKey(key("enter"))
	if cmd != nil {
		t.Fatal("a second enter while a create was in flight fired another create")
	}
	if got := out.(tuiModel).creator.step; got != stepNewRepoConfirm {
		t.Errorf("input during a create moved the wizard to %v", got)
	}
}

// A forced-public override must be surfaced, not swallowed.
//
// An org policy can make a repo public when private was requested; GitHub says
// so only in the create response. If the wizard walks on to the agent picker
// without a word, the first time the operator learns their code is public is
// when someone else reads it.
func TestAForcedPublicRepoIsReportedToTheOperator(t *testing.T) {
	m := newRepoModel()
	m.creator.step = stepNewRepoConfirm
	m.creator.newRepoName = "app"
	m.creator.newRepoPrivate = true
	m.creator.newRepoBusy = true

	out, _ := m.onGhRepoCreated(ghRepoCreatedMsg{
		repo: githubid.Repo{NameWithOwner: "octocat/app", URL: "https://github.com/octocat/app.git", Private: false},
	})
	notice := out.(tuiModel).lastNotice
	if !strings.Contains(notice, "PUBLIC") {
		t.Fatalf("a repo created public against an explicit private request produced "+
			"notice %q — the override is invisible", notice)
	}
}

// A failed create must leave the operator on the confirm screen with the reason,
// not drop them somewhere with a cleared form.
func TestAFailedCreateKeepsTheFormAndShowsWhy(t *testing.T) {
	m := newRepoModel()
	m.creator.step = stepNewRepoConfirm
	m.creator.newRepoName = "app"
	m.creator.newRepoBusy = true

	out, _ := m.onGhRepoCreated(ghRepoCreatedMsg{err: errCreateFailed{}})
	c := out.(tuiModel).creator
	if c.step != stepNewRepoConfirm {
		t.Errorf("a failed create moved to step %v", c.step)
	}
	if c.newRepoBusy {
		t.Error("still marked busy after a failure — the screen is now stuck")
	}
	if !strings.Contains(c.err, "already exists") {
		t.Errorf("the reason did not reach the operator: %q", c.err)
	}
	if c.newRepoName != "app" {
		t.Errorf("the typed name was lost on failure: %q", c.newRepoName)
	}
}

type errCreateFailed struct{}

func (errCreateFailed) Error() string { return "name already exists on this account" }
