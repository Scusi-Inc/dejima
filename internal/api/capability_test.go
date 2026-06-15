package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestCapabilityGrants drives grant → list → revoke through the real handlers,
// asserting deny-all storage, validation, and that each grant/revoke lands a
// fixed-schema entry in the hash-chained Ledger.
func TestCapabilityGrants(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	// grant
	rr := do(t, h, http.MethodPost, "/v1/islands/oc/capability/grants", `{"target":"MorningBrief"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	var gv CapabilityGrantView
	_ = json.Unmarshal(rr.Body.Bytes(), &gv)
	if gv.Target != "MorningBrief" {
		t.Errorf("grant view target = %q, want MorningBrief", gv.Target)
	}

	// duplicate → 409; invalid target → 400
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/capability/grants", `{"target":"MorningBrief"}`); rr.Code != http.StatusConflict {
		t.Errorf("duplicate grant: got %d, want 409", rr.Code)
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/capability/grants", `{"target":"a/b"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid target: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}

	// list shows the one grant
	rr = do(t, h, http.MethodGet, "/v1/islands/oc/capability/grants", "")
	var lr CapabilityGrantsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &lr)
	if len(lr.Grants) != 1 || lr.Grants[0].Target != "MorningBrief" {
		t.Fatalf("list = %+v, want one MorningBrief", lr.Grants)
	}

	// revoke → 204; revoking again → 404
	if rr := do(t, h, http.MethodDelete, "/v1/islands/oc/capability/grants/MorningBrief", ""); rr.Code != http.StatusNoContent {
		t.Errorf("revoke: got %d, want 204 (%s)", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/v1/islands/oc/capability/grants/MorningBrief", ""); rr.Code != http.StatusNotFound {
		t.Errorf("revoke missing: got %d, want 404", rr.Code)
	}

	// list empty after revoke
	rr = do(t, h, http.MethodGet, "/v1/islands/oc/capability/grants", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &lr)
	if len(lr.Grants) != 0 {
		t.Errorf("after revoke, list = %+v, want empty", lr.Grants)
	}

	// ledger recorded both the grant and the revoke
	rr = do(t, h, http.MethodGet, "/v1/audit", "")
	var ar AuditResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ar)
	var sawGrant, sawRevoke bool
	for _, e := range ar.Entries {
		if e.Island != "oc" || e.Scope != "MorningBrief" {
			continue
		}
		switch e.Type {
		case "capability.grant":
			sawGrant = true
		case "capability.revoke":
			sawRevoke = true
		}
	}
	if !sawGrant || !sawRevoke {
		t.Errorf("ledger missing entries: grant=%v revoke=%v", sawGrant, sawRevoke)
	}
	if !ar.Verified {
		t.Errorf("ledger chain failed to verify: %s", ar.Error)
	}
}

// A capability grant must never be reachable on the island token path — a
// contained brain cannot grant itself a capability (operator-only, like Port
// grants). The route is absent from tokenRouteAccess, so authorize denies it.
func TestCapabilityGrantNotTokenReachable(t *testing.T) {
	if err := authorizeToken("oc", "POST /v1/islands/{name}/capability/grants", "/v1/islands/oc/capability/grants"); err == nil {
		t.Fatal("capability grant must be denied on the token path")
	}
}
