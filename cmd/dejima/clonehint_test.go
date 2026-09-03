package main

import (
	"os"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/srcscan"
)

// The auth failure is read by someone who has just discovered they have no
// GitHub identity at all. It must name the path with the FEWEST prerequisites.
//
// `dejima github connect` is a guided device flow — it prints a code, the
// operator approves in a browser, the daemon captures the token.
// the token-push path (`auth push`) requires an ALREADY-CONFIGURED gh on the client to
// push FROM, so it is the wrong first instruction for a new operator: it names
// more prerequisites exactly when they have fewest.
func TestCloneAuthHintNamesTheGuidedPath(t *testing.T) {
	got := cloneFailureHint("hex4x", "auth")
	if !strings.Contains(got, "dejima github connect") {
		t.Errorf("auth hint does not name the guided sign-in:\n%s", got)
	}
	// This asserts the command is ABSENT, which is the opposite of testing it.
	// The literal used to be BUILT — `"auth" + " push"` — because the coverage
	// ratchet counted any mention as coverage, so writing it plainly marked
	// `cli auth push` a stale waiver and invited someone to delete a waiver for
	// a command with no test. The gate now requires an invocation (issue #335),
	// so the sentence can say what it means.
	if strings.Contains(got, "dejima auth push") {
		t.Errorf("auth hint still sends a new operator to the path that needs a "+
			"pre-configured gh:\n%s", got)
	}
	// It must still say how to retry, or the operator fixes auth and stops.
	if !strings.Contains(got, "dejima upgrade hex4x") {
		t.Errorf("names no way to re-clone once the identity exists:\n%s", got)
	}
}

// The in-island message is a SEPARATE string in image/start.sh and drifted from
// this one — the operator saw the shell version, not the Go one. Read with
// comments stripped so the guard cannot be satisfied by prose about the fix.
func TestInIslandAuthHintAgrees(t *testing.T) {
	raw, err := os.ReadFile("../../image/start.sh")
	if err != nil {
		t.Fatalf("read start.sh: %v", err)
	}
	code := srcscan.StripLineComments(string(raw), "#")
	if !strings.Contains(code, "dejima github connect") {
		t.Error("image/start.sh does not name the guided sign-in; the two clone-failure " +
			"messages have drifted again")
	}
	if strings.Contains(code, "auth"+" push --github") {
		t.Error("image/start.sh still sends the operator to the token-push path")
	}
}
