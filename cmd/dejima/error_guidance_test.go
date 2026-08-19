package main

import (
	"regexp"
	"strings"
	"testing"
)

// dejimaCmdRef matches a command suggestion inside user-facing text: the binary
// name followed by the verb it's being told to run. Prose ("the dejima daemon")
// would match too, so this only works on text that is overwhelmingly
// instructions — which the identity gate is. Widening it to arbitrary help text
// would need a narrower pattern first.
var dejimaCmdRef = regexp.MustCompile(`dejima ([a-z][a-z-]+)`)

// knownVerbs collects the TOP-LEVEL commands and aliases only, because that is
// what the token directly after "dejima " has to be. `dejima github connect`
// checks out on "github"; `dejima create …` must not, and the depth matters:
// `create` IS registered — as an alias of `term create` — so a walk over the
// whole tree accepts it and the guard silently stops guarding.
//
// That is not hypothetical. The first version of this test walked every depth,
// passed, and went on passing when the bug it was written for was reinstated.
func knownVerbs(t *testing.T) map[string]bool {
	t.Helper()
	verbs := map[string]bool{}
	for _, sub := range newRootCmd().Commands() {
		verbs[sub.Name()] = true
		for _, a := range sub.Aliases {
			verbs[a] = true
		}
	}
	if len(verbs) < 10 {
		t.Fatalf("only %d top-level commands found — the walk is broken, so this "+
			"test would pass by finding nothing", len(verbs))
	}
	if verbs["create"] {
		t.Fatal("`create` resolved as a top-level command; this test's whole " +
			"premise is that it is not one")
	}
	return verbs
}

// The GitHub identity gate is the error a first-run user hits when their repo
// isn't anonymously reachable, and it is almost entirely instructions. It used
// to end with:
//
//	dejima create … --force
//
// There is no `dejima create`. The island verb is `init`; `create` exists only
// as a subcommand alias under `term`. So the one place the message told the
// operator to DO something, it named a command that answers "unknown command"
// — in a first-run failure path, which is the worst possible place for it.
func TestIdentityGateGuidanceNamesRealCommands(t *testing.T) {
	cliEnv(t)
	verbs := knownVerbs(t)

	// A private/unreachable repo with no configured identity triggers the gate.
	_, err := runCLI(t, "init", "--repo", "https://github.com/a/b", "--name", "work", "--agent", "claude-code")
	if err == nil {
		t.Fatal("expected the identity gate to refuse an unreachable repo — " +
			"without the error there is no guidance to check and this test proves nothing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "needs a GitHub identity") {
		t.Fatalf("got a different error than the identity gate, so the guidance "+
			"below isn't being checked: %v", err)
	}

	var bad []string
	for _, m := range dejimaCmdRef.FindAllStringSubmatch(msg, -1) {
		if !verbs[m[1]] {
			bad = append(bad, m[1])
		}
	}
	if len(bad) > 0 {
		t.Errorf("guidance names commands that don't exist: %v\n\nfull message:\n%s",
			bad, msg)
	}
}
