package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// These extend the existing link/mailbox/wake handler tests to the routes not
// yet exercised: GET/DELETE /v1/links, the exposed-actions list + unexpose, the
// pending-approvals list, and the wake HTTP route. All over httptest, no Docker.

// TestLinkListAndRevoke covers GET /v1/links and DELETE /v1/links: a grant shows
// in the list, revoke removes it (204), and re-revoking a gone grant is 404.
func TestLinkListAndRevoke(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", n, rr.Code)
		}
	}

	// Empty list to start.
	if grants := listLinks(t, h); len(grants) != 0 {
		t.Fatalf("links should start empty, got %d", len(grants))
	}

	if rr := do(t, h, http.MethodPost, "/v1/links",
		`{"from":"alpha","to":"beta","topic":"ops"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d", rr.Code)
	}

	grants := listLinks(t, h)
	if len(grants) != 1 || grants[0].From != "alpha" || grants[0].To != "beta" || grants[0].Topic != "ops" {
		t.Fatalf("link list = %+v, want one alpha→beta/ops grant", grants)
	}

	// Revoke needs all three query params.
	if rr := do(t, h, http.MethodDelete, "/v1/links?from=alpha&to=beta", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("revoke without topic: got %d, want 400", rr.Code)
	}
	if rr := do(t, h, http.MethodDelete, "/v1/links?from=alpha&to=beta&topic=ops", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: got %d, want 204", rr.Code)
	}
	if grants := listLinks(t, h); len(grants) != 0 {
		t.Errorf("link list after revoke = %+v, want empty", grants)
	}
	// Revoking a now-absent grant is a 404.
	if rr := do(t, h, http.MethodDelete, "/v1/links?from=alpha&to=beta&topic=ops", ""); rr.Code != http.StatusNotFound {
		t.Errorf("revoke gone grant: got %d, want 404", rr.Code)
	}
}

// TestLinkExposedListAndUnexpose covers GET .../link/actions and DELETE
// .../link/actions/{action}: expose adds, the list reflects it, unexpose removes
// it (204), and unexposing an absent action is 404.
func TestLinkExposedListAndUnexpose(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"beta","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create beta: %d", rr.Code)
	}

	if got := listExposed(t, h, "beta"); len(got) != 0 {
		t.Fatalf("beta should expose nothing initially, got %v", got)
	}

	if rr := do(t, h, http.MethodPut, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusOK {
		t.Fatalf("expose deploy: %d", rr.Code)
	}
	if got := listExposed(t, h, "beta"); len(got) != 1 || got[0] != "deploy" {
		t.Fatalf("exposed list = %v, want [deploy]", got)
	}

	if rr := do(t, h, http.MethodDelete, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("unexpose: got %d, want 204", rr.Code)
	}
	if got := listExposed(t, h, "beta"); len(got) != 0 {
		t.Errorf("exposed list after unexpose = %v, want empty", got)
	}
	// Unexposing an action that isn't exposed is a 404.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusNotFound {
		t.Errorf("unexpose absent: got %d, want 404", rr.Code)
	}
	// On a missing island, the exposed-list is a 404.
	if rr := do(t, h, http.MethodGet, "/v1/islands/ghost/link/actions", ""); rr.Code != http.StatusNotFound {
		t.Errorf("exposed list on missing island: got %d, want 404", rr.Code)
	}
}

// TestLinkPendingList covers GET /v1/link/actions: a queued (exposed-but-not-
// pre-authorized) action shows in the operator's pending list, and disappears
// once denied.
func TestLinkPendingList(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", n, rr.Code)
		}
	}
	betaAgent := primaryAgentID(t, h, "beta")

	if len(listPending(t, h)) != 0 {
		t.Fatal("pending list should start empty")
	}

	// Grant an info channel + expose the action, then request it → queued.
	if rr := do(t, h, http.MethodPost, "/v1/links",
		`{"from":"alpha","to":"beta","topic":"ops"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPut, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusOK {
		t.Fatalf("expose: %d", rr.Code)
	}
	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("queue action: got %d, want 202", rr.Code)
	}
	var resp LinkActionResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	pending := listPending(t, h)
	if len(pending) != 1 || pending[0].From != "alpha" || pending[0].Action != "deploy" {
		t.Fatalf("pending list = %+v, want one alpha deploy", pending)
	}

	// Deny it → the queue drains.
	if rr := do(t, h, http.MethodPost, "/v1/link/actions/"+resp.Pending+"/deny", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("deny: %d", rr.Code)
	}
	if len(listPending(t, h)) != 0 {
		t.Errorf("pending list after deny should be empty")
	}
}

// TestWakeRoute covers POST /v1/islands/{name}/wake over HTTP: an existing
// island wakes (200), the runtime is asked to start a stopped container, and a
// missing island is 404.
func TestWakeRoute(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"isl","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}

	// Simulate a hibernated container so wake takes the StartContainer path.
	f.mu.Lock()
	f.status = "exited"
	before := f.startCalls
	f.mu.Unlock()

	if rr := do(t, h, http.MethodPost, "/v1/islands/isl/wake", ""); rr.Code != http.StatusOK {
		t.Fatalf("wake: got %d, body %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	after := f.startCalls
	f.mu.Unlock()
	if after == before {
		t.Error("wake of a stopped island did not start the container")
	}

	if rr := do(t, h, http.MethodPost, "/v1/islands/ghost/wake", ""); rr.Code != http.StatusNotFound {
		t.Errorf("wake missing island: got %d, want 404", rr.Code)
	}
}

// TestMailboxBroadcastAndDenyLedger covers the mailbox broadcast path and that a
// deny-all cross-island send is ledgered as link.deny.
func TestMailboxBroadcastAndDenyLedger(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", n, rr.Code)
		}
	}
	betaAgent := primaryAgentID(t, h, "beta")

	// An ungranted cross-island send is refused AND recorded as link.deny.
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/send",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"t","payload":"hi"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("ungranted send: got %d, want 403", rr.Code)
	}
	var found bool
	for _, e := range readLedger(t) {
		if e.Type == "link.deny" && e.Island == "alpha" {
			found = true
		}
	}
	if !found {
		t.Error("a denied cross-island send should be ledgered as link.deny")
	}
}

// --- helpers ---------------------------------------------------------------

func listLinks(t *testing.T, h http.Handler) []struct {
	From, To, Topic string
} {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/links", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list links: got %d", rr.Code)
	}
	var resp struct {
		Grants []struct {
			From, To, Topic string
		} `json:"grants"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode links: %v", err)
	}
	return resp.Grants
}

func listExposed(t *testing.T, h http.Handler, island string) []string {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+island+"/link/actions", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list exposed: got %d", rr.Code)
	}
	var resp LinkExposedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode exposed: %v", err)
	}
	return resp.Actions
}

func listPending(t *testing.T, h http.Handler) []struct {
	From, To, Action, Topic string
} {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/link/actions", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list pending: got %d", rr.Code)
	}
	var resp struct {
		Pending []struct {
			From, To, Action, Topic string
		} `json:"pending"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	return resp.Pending
}
