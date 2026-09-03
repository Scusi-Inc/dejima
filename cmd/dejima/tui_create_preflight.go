package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/githubid"
)

// The credential preflight: asking the GitHub question BEFORE the island is
// built rather than after the clone fails inside it.
//
// THE REPORT. An operator on a fresh host pasted a GitHub URL, watched an island
// get created and an image get pulled, and then got:
//
//	Error: clone failed (auth) — this island can't authenticate to the git remote.
//
// Raw git underneath was "Repository not found" plus "Authentication failed",
// which is what GitHub returns for a PRIVATE repo to an unauthenticated caller.
// The credential is a prerequisite of the very first choice in the flow and it
// was checked last — at the slowest step, in a terminal the operator has to go
// and read, rather than on the screen where the decision was made.
//
// WHICH PATH THIS IS, precisely, because the two source paths differ and only
// one of them was broken:
//
//   - THE BROWSER path names its identity. creatorSelectIdentity records the
//     identity whose repos are being listed, and CreateIslandRequest carries it,
//     so the credential that found the repo is the credential the island gets.
//     That path cannot produce this failure.
//   - THE PASTED-URL path names nothing. The daemon resolves whatever default
//     it holds, and when it holds none there is nothing to resolve — which is
//     invisible until the clone.
//
// WHAT THIS DELIBERATELY DOES NOT DO: block. A public repo clones perfectly well
// with no credential at all, and nothing here can tell public from private
// without asking GitHub for the repo — which is a network call, on a URL the
// operator may have typed wrong, to answer a question that only matters
// sometimes. So it states the uncertainty instead of resolving it, and lets the
// operator through. Guessing "this will fail" and being wrong teaches people to
// press past the warning, which costs more than the warning saves.

// looksLikeGitHubRemote reports whether a resolved repo will be cloned from
// GitHub over a network protocol — the only case where a missing identity can
// bite. A local path, a file:// source, or another forge is not this.
func looksLikeGitHubRemote(repo string) bool {
	r := strings.TrimSpace(strings.ToLower(repo))
	switch {
	case r == "":
		return false
	case strings.HasPrefix(r, "https://github.com/"), strings.HasPrefix(r, "http://github.com/"):
		return true
	case strings.HasPrefix(r, "git@github.com:"), strings.HasPrefix(r, "ssh://git@github.com/"):
		return true
	default:
		return false
	}
}

// islandWillGetAnIdentity reports whether a create that names NO identity will
// resolve one, given the identities the daemon reports.
//
// Mirrors the daemon's resolution rather than re-deriving it: an unnamed create
// takes the default. An identity that is not the default is not reachable by a
// request that does not name it, however many of them exist — which is the state
// the GitHub pane already warns about separately ("the default is used by NO
// island"), one surface over.
func islandWillGetAnIdentity(ids []githubid.Meta) bool {
	for _, id := range ids {
		if id.Default {
			return true
		}
	}
	return false
}

// creatorPreflightGitHub decides whether a pasted GitHub URL needs the operator
// to be told about the credential before anything is built. It returns the next
// step, and whether a preflight is warranted at all.
func creatorPreflightGitHub(repo string, ids []githubid.Meta, loaded bool) (warn bool) {
	if !looksLikeGitHubRemote(repo) {
		return false
	}
	// Not knowing is not the same as knowing there is none. If the identity list
	// never loaded — an older daemon, a failed call — say nothing rather than
	// warning about a credential that may well be there. A false warning on the
	// happy path is how a gate gets ignored on the unhappy one.
	if !loaded {
		return false
	}
	return !islandWillGetAnIdentity(ids)
}

// renderGitHubPreflight is the screen itself. It names what is certain, what is
// not, and both ways out.
func renderGitHubPreflight(repo string) string {
	var b strings.Builder
	b.WriteString(styleWaiting.Render("⚠  No GitHub identity is connected"))
	b.WriteString("\n\n")
	b.WriteString("  " + styleMuted.Render("about to clone") + "  " + styleAccent.Render(repo) + "\n\n")
	// The honest split. We know there is no identity; we do not know whether this
	// repo needs one, and saying so is cheaper than a network call to find out.
	b.WriteString("  " + styleMuted.Render("A PUBLIC repo clones fine without one.") + "\n")
	b.WriteString("  " + styleMuted.Render("A PRIVATE one fails — after the island is built and the image pulled.") + "\n\n")
	b.WriteString(styleMuted.Render("  [c] connect GitHub first   [enter] continue anyway   [esc] back"))
	return b.String()
}

// creatorGitHubPreflightKey drives that screen.
func (m tuiModel) creatorGitHubPreflightKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.creator
	switch msg.String() {
	case "c":
		// Straight into the in-pane sign-in, not a printed command and not a
		// spawned window. The creator closes: connecting an identity is the
		// prerequisite, not a step of this wizard, and resuming a wizard into a
		// stale answer is worse than starting it again.
		m.creator = nil
		return m.openGithubViewConnecting("")
	case "enter":
		// Through, deliberately. See the file comment: we cannot tell public from
		// private, and a warning that blocks the common case gets routed around.
		return m.creatorEnterAgent(c.preflightName)
	case "esc", "ctrl+[":
		c.step = stepManual
		return m, nil
	}
	return m, nil
}
