package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The island primer is the ONLY context most agents get about what they can do
// here. It is written into every agent's global instructions file at launch, so
// a false claim in it is not a documentation bug — it is a false belief held by
// every agent in every island, and it fails silently: an agent told it CANNOT do
// something never tries, so the capability is invisible to exactly the
// population that would use it.
//
// That happened. The primer said an island token "CANNOT create islands, reach
// other islands, or touch the control plane", while tokenauth.go grants
// accessOwnIsland on POST /link/send. Cross-island messaging worked and every
// agent had been told it was impossible by design. Nobody noticed for as long as
// the feature has existed, because the failure mode is agents not asking.
//
// So: every capability the token table GRANTS to an island token must be
// mentioned in the primer. This cannot check that the prose is true — no test
// can — but it catches the specific failure of a capability agents have and are
// never told about.
func TestPrimerMentionsEveryIslandTokenCapability(t *testing.T) {
	primer, err := os.ReadFile(filepath.Join("..", "..", "image", "island-primer.md"))
	if err != nil {
		t.Fatalf("read primer: %v", err)
	}
	text := strings.ToLower(string(primer))
	if len(text) < 500 {
		t.Fatalf("primer is %d bytes — too short to be the real file, so any "+
			"agreement below would be vacuous", len(text))
	}

	// Route prefix → the command an agent would run, as the primer must name it.
	// Only routes an island token can actually reach: a capability it does not
	// have does not belong in its instructions.
	capabilities := map[string]string{
		"/link/send":   "dejima link",
		"/link/action": "dejima link",
		"/mailbox":     "dejima msg",
		"/secrets":     "dejima secret",
		"/port/":       "dejima port",
	}

	granted := 0
	for route, access := range tokenRouteAccess {
		if access == accessDeny {
			continue
		}
		for suffix, command := range capabilities {
			if !strings.Contains(route, suffix) {
				continue
			}
			granted++
			if !strings.Contains(text, command) {
				t.Errorf("an island token is granted %q (%v) but the primer never "+
					"mentions %q.\nAgents are told what they can do ONLY by this file. "+
					"A capability that is granted and unmentioned is one no agent will "+
					"ever use.", route, access, command)
			}
		}
	}
	if granted == 0 {
		t.Fatal("matched no granted routes at all — the route table was renamed or " +
			"restructured, and this guard is now checking nothing while passing")
	}
}
