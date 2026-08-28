package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
)

func view(name string, def bool, islands ...string) api.GitHubIdentityView {
	return api.GitHubIdentityView{
		Meta:    githubid.Meta{Name: name, Login: "aoos", Default: def},
		Islands: islands,
	}
}

// The incident, reduced to a table.
//
// Two identities for the SAME GitHub login. The default was refreshed, correctly,
// and eight islands kept failing — because all eight pinned the other one and
// nothing at all used the default. Every field in the old listing agreed between
// the two rows; the `*` pointed at the one that mattered least.
func TestSplitByUsageWarnsOnlyOnTheMisdirection(t *testing.T) {
	tests := []struct {
		name       string
		ids        []api.GitHubIdentityView
		def        string
		wantUnused string
		wantUsed   string
	}{{
		name:       "the incident: default used by nobody, another used by eight",
		ids:        []api.GitHubIdentityView{view("github", true), view("aoos", false, "a", "b", "c", "d", "e", "f", "g", "h")},
		def:        "github",
		wantUnused: "github",
		wantUsed:   "aoos",
	}, {
		// Must NOT fire. This is the healthy majority case, and a warning here
		// trains people to skip the one that matters.
		name: "default is what the islands use",
		ids:  []api.GitHubIdentityView{view("github", true, "a", "b"), view("aoos", false)},
		def:  "github",
	}, {
		// Must NOT fire. A daemon with identities and no islands is not misdirected,
		// it is new.
		name: "nothing uses anything yet",
		ids:  []api.GitHubIdentityView{view("github", true), view("aoos", false)},
		def:  "github",
	}, {
		// Must NOT fire: one identity, unused, and it IS the default. There is
		// nothing to repoint to, so the advice would be unfollowable.
		name: "a single unused identity",
		ids:  []api.GitHubIdentityView{view("github", true)},
		def:  "github",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			unused, used := splitByUsage(tc.ids, tc.def)
			if unused != tc.wantUnused || used != tc.wantUsed {
				t.Errorf("splitByUsage() = (%q, %q), want (%q, %q)", unused, used, tc.wantUnused, tc.wantUsed)
			}
		})
	}
}

// A zero timestamp means the daemon does not know when the token was written —
// identities that predate the field. It must not render as "just now" (which
// would say a month-dead credential is fresh) or as 1970.
func TestRefreshedAgeAdmitsWhatItDoesNotKnow(t *testing.T) {
	if got := refreshedAge(time.Time{}); got != "unknown" {
		t.Errorf("a zero time rendered as %q — an operator reads this column to decide "+
			"whether a credential is plausibly dead, and a confident wrong answer is "+
			"worse than none", got)
	}
	if got := refreshedAge(time.Now().Add(-72 * time.Hour)); !strings.Contains(got, "3d") {
		t.Errorf("refreshedAge(3 days ago) = %q, want it to mention 3d", got)
	}
}

// A truncated island list must never be readable as the complete one: the count
// is exact and comes first, and the elision is explicit.
func TestIslandsCellNeverUnderstatesTheCount(t *testing.T) {
	got := islandsCell([]string{"a", "b", "c", "d", "e", "f", "g", "h"})
	if !strings.HasPrefix(got, "8 ") {
		t.Errorf("islandsCell(8 islands) = %q, want the exact count first", got)
	}
	if !strings.Contains(got, "+5 more") {
		t.Errorf("islandsCell(8 islands) = %q, want the elision stated", got)
	}
	if got := islandsCell(nil); !strings.Contains(got, "none") {
		t.Errorf("islandsCell(nil) = %q, want it to say none — a blank cell reads as "+
			"missing data, and 'nothing uses this' is the finding", got)
	}
}

// A token that AUTHENTICATES and cannot do the work must not read as healthy.
//
// The three states are distinct and collapsing any two is the bug: a
// fine-grained token reports NO scopes (GitHub sends no header for them), which
// is not the same fact as a classic token that has none. Calling the first
// "no permissions" would condemn a working token; calling the second "unknown"
// would bless a broken one.
func TestScopeNoteSeparatesUnknownFromUnable(t *testing.T) {
	tests := []struct {
		name     string
		scopes   string
		wantOK   bool
		contains string
	}{
		{"fine-grained sends no header", "", true, "fine-grained"},
		{"classic with repo can write", "repo, read:org", true, "repo"},
		{"classic without repo cannot", "read:org, gist", false, "no `repo` scope"},
		{"read-only repo scopes are not repo", "public_repo, read:org", false, "no `repo` scope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			note, ok := githubid.ScopeNote(tc.scopes)
			if ok != tc.wantOK {
				t.Errorf("ScopeNote(%q) canWrite = %v, want %v", tc.scopes, ok, tc.wantOK)
			}
			if !strings.Contains(note, tc.contains) {
				t.Errorf("ScopeNote(%q) note = %q, want it to mention %q", tc.scopes, note, tc.contains)
			}
		})
	}
}
