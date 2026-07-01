package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/authtoken"
)

// TestAggregateHostWide (P3): /v1/aggregate is host-wide (counts ALL islands
// regardless of owner), readable by any authenticated caller, and leaks NO
// names/repos/owners — the contrast with the owner-scoped /v1/overview.
func TestAggregateHostWide(t *testing.T) {
	h, _ := newTestServer(t)
	amanda, _, err := authtoken.Create("amanda", authtoken.RoleOperator, nil, "amanda")
	if err != nil {
		t.Fatal(err)
	}

	// One host-owned island, one amanda-owned island.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"secret-repo","name":"hostisle","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create hostisle: %d %s", rr.Code, rr.Body)
	}
	if rr := doAuth(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"amandaisle","agent":"claude-code"}`, amanda); rr.Code != http.StatusCreated {
		t.Fatalf("amanda create: %d %s", rr.Code, rr.Body)
	}

	// Amanda's OVERVIEW is owner-scoped (1); her AGGREGATE is host-wide (2).
	var ov OverviewResponse
	_ = json.Unmarshal(doAuth(t, h, http.MethodGet, "/v1/overview", "", amanda).Body.Bytes(), &ov)
	if ov.TotalIslands != 1 {
		t.Errorf("amanda overview total = %d, want 1 (owner-scoped)", ov.TotalIslands)
	}

	rr := doAuth(t, h, http.MethodGet, "/v1/aggregate", "", amanda)
	if rr.Code != http.StatusOK {
		t.Fatalf("amanda aggregate: %d (any authed caller should read it)", rr.Code)
	}
	var ag AggregateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ag)
	if ag.TotalIslands != 2 {
		t.Errorf("aggregate total = %d, want 2 (host-wide, not owner-filtered)", ag.TotalIslands)
	}
	// No names / repos / owners ever leak — the whole point.
	body := rr.Body.String()
	for _, leak := range []string{"hostisle", "amandaisle", "secret-repo", "amanda", "aoos", "owner"} {
		if strings.Contains(body, leak) {
			t.Errorf("aggregate response leaked %q: %s", leak, body)
		}
	}

	// Host owner sees the same host-wide total.
	_ = json.Unmarshal(do(t, h, http.MethodGet, "/v1/aggregate", "").Body.Bytes(), &ag)
	if ag.TotalIslands != 2 {
		t.Errorf("host-owner aggregate total = %d, want 2", ag.TotalIslands)
	}
}
