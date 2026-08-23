package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
)

// Removing a GitHub identity is not reversible from the CLI — the token is gone
// — so `rm` works out the consequences BEFORE deleting and lets the operator
// decline. The daemon reports affected islands only after the delete, which is
// too late to say no.

func idents(names ...string) []githubid.Meta {
	out := make([]githubid.Meta, 0, len(names))
	for _, n := range names {
		m := githubid.Meta{Name: strings.TrimPrefix(n, "*"), Login: "user"}
		m.Default = strings.HasPrefix(n, "*")
		out = append(out, m)
	}
	return out
}

func islandsUsing(pairs ...string) []api.IslandInfo {
	var out []api.IslandInfo
	for _, p := range pairs {
		name, ident, _ := strings.Cut(p, "=")
		out = append(out, api.IslandInfo{Name: name, GitHubIdentity: ident})
	}
	return out
}

func TestRemovalIsUnblockedWhenNothingDependsOnIt(t *testing.T) {
	got := removalBlockers("spare", idents("*aoos", "spare"), islandsUsing("alpha=aoos"), false)
	if len(got) != 0 {
		t.Errorf("an unused, non-default identity should remove cleanly, got %v", got)
	}
}

func TestRemovalBlockedByIslandsUsingIt(t *testing.T) {
	got := removalBlockers("aoos", idents("*aoos"), islandsUsing("alpha=aoos", "beta=aoos", "gamma=other"), false)
	if len(got) == 0 {
		t.Fatal("islands using the identity must block")
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the blocker must NAME the islands so the operator can act; %q missing from %q", want, joined)
		}
	}
	if strings.Contains(joined, "gamma") {
		t.Errorf("an island on a DIFFERENT identity must not be listed: %q", joined)
	}
}

// Removing the default silently leaving none is the false-surface shape: every
// later lookup resolves nothing, with no error at the moment of the change.
func TestRemovalBlockedWhenItIsTheDefaultAndOthersExist(t *testing.T) {
	got := removalBlockers("aoos", idents("*aoos", "github"), nil, false)
	if len(got) != 1 || !strings.Contains(got[0], "DEFAULT") {
		t.Fatalf("removing the default must be flagged, got %v", got)
	}
	if !strings.Contains(got[0], "dejima github default") {
		t.Error("the blocker must name the command that fixes it first")
	}
}

// ...but the LAST identity being the default is not a reason to refuse. There is
// no other to point at, and blocking would leave the operator unable to clear a
// dead credential — a guard that only obstructs.
func TestRemovingTheLastIdentityIsNotBlockedForBeingDefault(t *testing.T) {
	got := removalBlockers("aoos", idents("*aoos"), nil, false)
	for _, b := range got {
		if strings.Contains(b, "DEFAULT") {
			t.Errorf("the only identity should not be blocked for being the default: %q", b)
		}
	}
}

// "I couldn't check" must not read as "nothing to worry about".
func TestRemovalReportsAFailedPreflightAsABlocker(t *testing.T) {
	got := removalBlockers("aoos", nil, nil, true)
	if len(got) != 1 {
		t.Fatalf("an unanswered lookup must produce a blocker, got %v", got)
	}
	if !strings.Contains(got[0], "unknown rather than none") {
		t.Errorf("the blocker must say the consequences are UNKNOWN, not absent: %q", got[0])
	}
}

// The control: the three conditions must stay distinguishable. Each test above
// passing alone would not catch two of them collapsing into one message.
func TestRemovalBlockersStayDistinct(t *testing.T) {
	cases := map[string][]string{
		"in use":  removalBlockers("aoos", idents("aoos", "x"), islandsUsing("alpha=aoos"), false),
		"default": removalBlockers("aoos", idents("*aoos", "x"), nil, false),
		"unknown": removalBlockers("aoos", nil, nil, true),
	}
	seen := map[string]string{}
	for label, got := range cases {
		if len(got) != 1 {
			t.Fatalf("%s should produce exactly one blocker, got %v", label, got)
		}
		if prev, dup := seen[got[0]]; dup {
			t.Errorf("%q and %q produce the same message — the operator cannot tell them apart", prev, label)
		}
		seen[got[0]] = label
	}
}

// The commands themselves, against the in-proc daemon. The unit tests above
// cover the pre-flight's reasoning; these cover that the verbs are wired, parse
// their arguments, and reach the API — the half a pure-function test cannot see.

func TestCLIGithubIdentityListEmpty(t *testing.T) {
	cliEnv(t)
	out, err := runCLI(t, "github", "ls")
	if err != nil {
		t.Fatalf("github ls: %v", err)
	}
	if !strings.Contains(out, "no GitHub identities") {
		t.Errorf("an empty list should say so, and say how to add one: %q", out)
	}
	if !strings.Contains(out, "connect --default") {
		t.Errorf("the empty-list hint must name --default, since plain connect is what "+
			"leaves an operator with an unused identity: %q", out)
	}
}

// Naming an identity that does not exist must fail, not silently create one:
// setting a default is a choice among things you have.
func TestCLIGithubDefaultRejectsAnUnknownIdentity(t *testing.T) {
	cliEnv(t)
	if _, err := runCLI(t, "github", "default", "nope"); err == nil {
		t.Error("setting the default to an unknown identity should fail")
	}
}

func TestCLIGithubRemoveRejectsAnUnknownIdentity(t *testing.T) {
	cliEnv(t)
	if _, err := runCLI(t, "github", "rm", "nope"); err == nil {
		t.Error("removing an identity that does not exist should fail")
	}
}
