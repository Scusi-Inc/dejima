package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
)

func ghView(name string, def bool, age time.Duration, islands ...string) api.GitHubIdentityView {
	m := githubid.Meta{Name: name, Login: "aoos", Default: def}
	if age > 0 {
		m.UpdatedAt = time.Now().Add(-age)
	}
	return api.GitHubIdentityView{Meta: m, Islands: islands}
}

// The pane MANUFACTURED this incident. [c] ran a bare `dejima github connect`,
// which stores under the fixed name "github" and does not take the default. An
// operator whose `aoos` token had expired pressed [c] here to fix it, gained a
// second identity for the same login that no island used, refreshed that, and
// watched eight islands go on failing.
//
// So [c] must name the identity it is refreshing.
func TestGithubPaneRefreshesTheSelectedIdentityByName(t *testing.T) {
	v := &githubView{identities: []api.GitHubIdentityView{
		ghView("github", true, 3*time.Hour),
		ghView("aoos", false, 31*24*time.Hour, "a", "b"),
	}, cursor: 1}
	m := tuiModel{github: v}

	// canOpenNewWindow() is false under test, so the notice carries the exact
	// command — which is the string a person would copy. It must be right.
	out, _ := m.githubKey(key("c"))
	notice := out.(tuiModel).github.notice
	if !strings.Contains(notice, "connect aoos --default") {
		t.Errorf("pressing [c] on the selected identity offered %q — it must name the\n"+
			"identity being refreshed and take the default, or it silently creates a\n"+
			"SECOND identity for the same login that nothing uses", notice)
	}
}

// The FIRST identity must become the default, or the daemon ends up holding
// credentials with no default — in which case anything that does not name an
// identity resolves nothing at all, and every island clone fails.
//
// This path is only reachable with an empty list, so it is invisible to any test
// that starts from a populated pane. A mutation found it, not review.
func TestGithubPaneFirstIdentityTakesTheDefault(t *testing.T) {
	m := tuiModel{github: &githubView{}}
	out, _ := m.githubKey(key("c"))
	notice := out.(tuiModel).github.notice
	if !strings.Contains(notice, "--default") {
		t.Errorf("connecting the FIRST identity offered %q — without --default the daemon "+
			"holds a credential and has no default, so nothing that omits a name "+
			"resolves anything", notice)
	}
}

// The header row must not assert health it never checked. Every row used to
// carry a ✓, including one whose token had been dead a month — right next to the
// identity the operator most needed to distrust.
func TestGithubPaneMarksTheUnusedDefault(t *testing.T) {
	m := tuiModel{github: &githubView{identities: []api.GitHubIdentityView{
		ghView("github", true, 3*time.Hour),
		ghView("aoos", false, 31*24*time.Hour, "a", "b", "c"),
	}}}
	out := m.renderGithubView()
	if strings.Contains(out, "✓") && strings.Contains(out, "github") {
		// A ✓ may legitimately appear on the USED identity; what must not happen
		// is the unused default reading as healthy.
		idx := strings.Index(out, "github")
		if idx > 0 && strings.Contains(out[max(0, idx-12):idx], "✓") {
			t.Error("the unused default still renders with a ✓")
		}
	}
	if !strings.Contains(out, "used by NO island") {
		t.Error("the pane does not say the default is used by nobody — the one fact " +
			"that would have ended the incident in ten seconds")
	}
	if !strings.Contains(out, "3 ") {
		t.Errorf("the pane does not show how many islands use each identity:\n%s", out)
	}
	if !strings.Contains(out, "31d ago") {
		t.Errorf("the pane does not show token age, so two identities for the same "+
			"login stay indistinguishable:\n%s", out)
	}
}

// An island pinned to a deleted identity has NO credential — a different state
// from an expired token, with a different fix, and identical symptoms.
func TestGithubPaneSurfacesDanglingPins(t *testing.T) {
	m := tuiModel{github: &githubView{
		identities: []api.GitHubIdentityView{ghView("aoos", true, time.Hour, "a")},
		dangling:   []api.DanglingIdentityPin{{Island: "krieg", Identity: "gone"}},
	}}
	out := m.renderGithubView()
	if !strings.Contains(out, "krieg") || !strings.Contains(out, "repoint") {
		t.Errorf("a dangling pin is not surfaced with its fix:\n%s", out)
	}
}
