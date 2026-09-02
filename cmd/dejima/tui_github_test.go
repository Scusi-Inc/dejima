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

	// The sign-in now runs IN the pane rather than in a spawned window, so the
	// thing to check is the flow it starts rather than the command it printed.
	// The incident is unchanged: a connect that does not name the highlighted
	// identity silently creates a second one for the same login.
	out, _ := m.githubKey(key("c"))
	f := out.(tuiModel).github.connect
	if f == nil {
		t.Fatal("pressing [c] started no sign-in at all")
	}
	if f.name != "aoos" {
		t.Errorf("pressing [c] on the selected identity would store under %q — it must "+
			"name the identity being refreshed, or it silently creates a SECOND "+
			"identity for the same login that nothing uses", f.name)
	}
	if !f.makeDefault {
		t.Error("the refresh does not take the default, which is the other half of the " +
			"same incident: eight islands went on failing because the refreshed identity " +
			"was not the one anything resolved to")
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
	f := out.(tuiModel).github.connect
	if f == nil {
		t.Fatal("pressing [c] on an empty pane started no sign-in at all")
	}
	if !f.makeDefault {
		t.Error("connecting the FIRST identity does not take the default — the daemon " +
			"then holds a credential with no default, so anything that omits a name " +
			"resolves nothing and every island clone fails")
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
