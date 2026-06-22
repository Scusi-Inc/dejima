package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestLinkActionGate exercises the Phase 3 action-delegation gate end to end on
// the operator path: deny-all, exposed-action requirement, pre-authorized
// execute, the approval queue, approve→deliver, and deny.
func TestLinkActionGate(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: got %d", n, rr.Code)
		}
	}
	betaAgent := primaryAgentID(t, h, "beta")

	// No grant → action refused (deny-all).
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("action with no grant: got %d, want 403", rr.Code)
	}

	// Grant an info channel (no pre-authorized actions) alpha→beta/ops.
	if rr := do(t, h, http.MethodPost, "/v1/links",
		`{"from":"alpha","to":"beta","topic":"ops"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: got %d", rr.Code)
	}

	// Action not exposed by beta → refused even with a channel grant.
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("unexposed action: got %d, want 403", rr.Code)
	}

	// Beta exposes "deploy".
	if rr := do(t, h, http.MethodPut, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusOK {
		t.Fatalf("expose: got %d", rr.Code)
	}

	// Exposed but NOT pre-authorized on the grant → goes to the approval queue.
	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy","from_agent":"a1"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("queued action: got %d, want 202", rr.Code)
	}
	var resp LinkActionResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "pending" || resp.Pending == "" {
		t.Fatalf("expected pending with id, got %+v", resp)
	}
	// Nothing delivered yet.
	if got := pollMailboxAs(t, h, "beta", betaAgent); len(got) != 0 {
		t.Fatalf("nothing should be delivered before approval; got %d", len(got))
	}

	// Operator approves → executes → delivered as a typed action with cross-island Origin.
	if rr := do(t, h, http.MethodPost, "/v1/link/actions/"+resp.Pending+"/approve", ""); rr.Code != http.StatusOK {
		t.Fatalf("approve: got %d", rr.Code)
	}
	msgs := pollMailboxAs(t, h, "beta", betaAgent)
	if len(msgs) != 1 || msgs[0].Action == nil || msgs[0].Action.Type != "deploy" {
		t.Fatalf("approved action not delivered as typed action: %+v", msgs)
	}
	if msgs[0].Origin == nil || !msgs[0].Origin.CrossIsland {
		t.Errorf("delivered action missing cross-island Origin: %+v", msgs[0].Origin)
	}

	// A pre-authorized action executes immediately (no queue).
	if rr := do(t, h, http.MethodPost, "/v1/links",
		`{"from":"alpha","to":"beta","topic":"ci","actions":["build"]}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant w/ actions: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPut, "/v1/islands/beta/link/actions/build", ""); rr.Code != http.StatusOK {
		t.Fatalf("expose build: got %d", rr.Code)
	}
	rr = do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ci","action":"build"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("pre-authorized action: got %d, want 201", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "executed" {
		t.Errorf("pre-authorized action status = %q, want executed", resp.Status)
	}

	// Deny path: queue another deploy, deny it, confirm nothing new is delivered.
	rr = do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy"}`)
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	before := len(pollMailboxAs(t, h, "beta", betaAgent))
	if rr := do(t, h, http.MethodPost, "/v1/link/actions/"+resp.Pending+"/deny", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("deny: got %d", rr.Code)
	}
	if after := len(pollMailboxAs(t, h, "beta", betaAgent)); after != before {
		t.Errorf("deny delivered something: before=%d after=%d", before, after)
	}
	// Approving an already-taken (denied) id fails closed.
	if rr := do(t, h, http.MethodPost, "/v1/link/actions/"+resp.Pending+"/approve", ""); rr.Code != http.StatusNotFound {
		t.Errorf("approve of denied id: got %d, want 404", rr.Code)
	}
}
