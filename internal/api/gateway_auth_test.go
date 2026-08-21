package api

import (
	"strings"
	"testing"
)

// Two auth decisions on the gateway route, both deliberate and both easy to get
// wrong by omission — which is why they are asserted rather than left to the
// defaults that happen to be right.

// An unlisted route is owner-only by default. That is SAFE and almost certainly
// not the intent for a console a non-owner operator is expected to use, so the
// route is classified consciously. This test fails if someone adds a verb to the
// mux and forgets the table — the failure mode being a console that silently
// works only for the owner.
func TestGatewayRouteIsClassifiedForEveryVerbItServes(t *testing.T) {
	const path = "/v1/islands/{name}/agents/{id}/gateway/{path...}"
	for _, verb := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		cap, ok := roleRouteCap[verb+" "+path]
		if !ok {
			t.Errorf("%s %s is served but not classified in roleauth — it would default to "+
				"owner-only, so the console would work for the owner and silently 403 for "+
				"every other operator", verb, path)
			continue
		}
		if cap != capOperate {
			t.Errorf("%s gateway = %v, want capOperate: sending work to an agent is an act, "+
				"not a read, and owner-only is not the intent", verb, cap)
		}
	}
}

// Readiness is an observation — it opens a connection and closes it without
// sending anything — so a viewer may ask.
func TestGatewayReadinessIsAReadNotAnAct(t *testing.T) {
	cap, ok := roleRouteCap["GET /v1/islands/{name}/agents/{id}/gateway-ready"]
	if !ok {
		t.Fatal("gateway-ready is not classified in roleauth")
	}
	if cap != capRead {
		t.Errorf("gateway-ready = %v, want capRead — probing readiness sends no work", cap)
	}
}

// THE CONTAINMENT BOUNDARY. An island token must never reach this route: an
// island driving another island's assistant through the daemon is exactly the
// break the token scoping exists to prevent.
//
// tokenauth denies by default, so the assertion is that the route is ABSENT from
// the token table — and absence is the kind of thing that gets added later by
// someone solving a different problem.
func TestGatewayRouteIsUnreachableByAnIslandToken(t *testing.T) {
	for route := range tokenRouteAccess {
		if strings.Contains(route, "/gateway") {
			t.Errorf("%q is reachable by an island token. An island reaching an assistant "+
				"through the daemon — its own or another's — is the containment break the "+
				"token scoping exists to prevent", route)
		}
	}
}

// The control on the test above. It asserts an ABSENCE, so it would pass just as
// well against an empty table, a renamed table, or a table this test can no
// longer see. Require it to be reading a populated one.
func TestTokenRouteTableIsPopulated(t *testing.T) {
	if len(tokenRouteAccess) == 0 {
		t.Fatal("tokenRouteAccess is empty — the absence test above proves nothing")
	}
	if _, ok := tokenRouteAccess["POST /v1/islands/{name}/mailbox"]; !ok {
		t.Fatal("a known island-token route is missing — this test is not reading the table it thinks it is")
	}
}
