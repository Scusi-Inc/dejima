package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSpawnGrantCRUD(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"alpha","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: got %d", rr.Code)
	}

	// Deny default: no grant.
	rr := do(t, h, http.MethodGet, "/v1/islands/alpha/spawn-grant", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get (none): %d", rr.Code)
	}
	var none SpawnGrantResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &none)
	if none.Granted {
		t.Fatal("fresh island must not be granted (deny default)")
	}

	// max_concurrent <= 0 rejected.
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant", `{"max_concurrent":0}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("zero max_concurrent should 400, got %d", rr.Code)
	}

	// Grant with a budget.
	rr = do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant",
		`{"max_concurrent":3,"max_total":10,"types":["claude-code"],"ttl":"30m","per_agent_memory":"512m"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("grant: %d", rr.Code)
	}
	var g SpawnGrantResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &g)
	if !g.Granted || g.Grant == nil || g.Grant.MaxConcurrent != 3 || g.Grant.PerAgentMemory != "512m" {
		t.Fatalf("grant response wrong: %+v", g)
	}

	// GET reflects it.
	rr = do(t, h, http.MethodGet, "/v1/islands/alpha/spawn-grant", "")
	var got SpawnGrantResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if !got.Granted || got.Grant.MaxTotal != 10 {
		t.Fatalf("get after grant wrong: %+v", got)
	}

	// Revoke.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/alpha/spawn-grant", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rr.Code)
	}
	rr = do(t, h, http.MethodGet, "/v1/islands/alpha/spawn-grant", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Granted {
		t.Fatal("grant should be gone after revoke")
	}
	// Revoking again → 404.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/alpha/spawn-grant", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("revoke-none should 404, got %d", rr.Code)
	}
}

// TestSpawnGrantIsOperatorOnly is guardrail 1: an in-island token can NEVER grant
// spawn — the grant-mutating routes must not be in the in-island token allow-list
// (tokenRouteAccess), so a contained agent can't grant itself spawn rights.
func TestSpawnGrantIsOperatorOnly(t *testing.T) {
	for _, route := range []string{
		"POST /v1/islands/{name}/spawn-grant",
		"DELETE /v1/islands/{name}/spawn-grant",
	} {
		if _, ok := tokenRouteAccess[route]; ok {
			t.Errorf("%s must NOT be reachable by an in-island token (operator-only grant)", route)
		}
		if roleRouteCap[route] != capOperate {
			t.Errorf("%s must require capOperate, got %v", route, roleRouteCap[route])
		}
	}
}
