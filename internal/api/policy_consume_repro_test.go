package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Reproduces a2's v0.6.3 live-test report: an auto-approve rule's `used` counter
// not incrementing on an auto-approved action. Drives the exact live path —
// grant (no static allowlist) → expose → add rule → requestAction → GET /v1/policy.
func TestPolicyRuleUsedIncrementsViaActionGate(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", n, rr.Code)
		}
	}
	betaAgent := primaryAgentID(t, h, "beta")

	// Grant WITHOUT a static action allowlist (so the rule, not the allowlist, is
	// what auto-approves) + expose deploy.
	if rr := do(t, h, http.MethodPost, "/v1/links", `{"from":"alpha","to":"beta","topic":"ops"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPut, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusOK {
		t.Fatalf("expose: %d", rr.Code)
	}
	// Add the auto-approve rule (max 20).
	if rr := do(t, h, http.MethodPost, "/v1/policy",
		`{"from":"alpha","to":"beta","action":"deploy","max_count":20,"ttl":"1h"}`); rr.Code != http.StatusCreated {
		t.Fatalf("policy add: %d %s", rr.Code, rr.Body.String())
	}

	// Two requests — each must AUTO-APPROVE (201 executed), not queue (202).
	for i := 1; i <= 2; i++ {
		rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
			`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy"}`)
		if rr.Code != http.StatusCreated {
			t.Fatalf("request %d: got %d (want 201 auto-approved by the rule); body %s", i, rr.Code, rr.Body.String())
		}
	}

	// The rule's used must reflect both auto-approvals.
	rr := do(t, h, http.MethodGet, "/v1/policy", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("policy ls: %d", rr.Code)
	}
	var pr PolicyListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Rules) != 1 {
		t.Fatalf("want 1 rule, got %+v", pr.Rules)
	}
	if pr.Rules[0].Used != 2 {
		t.Errorf("BUG REPRODUCED: rule.Used = %d, want 2 — auto-approve isn't consuming budget", pr.Rules[0].Used)
	}
}
