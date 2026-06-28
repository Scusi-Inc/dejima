package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// spawnReq drives POST /agents AS AN IN-ISLAND TOKEN caller by injecting the
// token-island into the request context (what tokenAuth does on the token
// listener) — so addAgent treats it as an agent-initiated spawn and applies the
// gate.
func spawnReq(h http.Handler, island, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/islands/"+island+"/agents", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), tokenIslandKey{}, island))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func ephemeralCount(t *testing.T, h http.Handler, island string) int {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+island, "")
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	n := 0
	for _, a := range info.Agents {
		if a.Ephemeral {
			n++
		}
	}
	return n
}

func TestSpawnGate_DenyWithoutGrant(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	// No grant → a token spawn is refused.
	if rr := spawnReq(h, "alpha", `{"type":"claude-code","ephemeral":true}`); rr.Code != http.StatusForbidden {
		t.Fatalf("spawn without grant: got %d, want 403", rr.Code)
	}
}

func TestSpawnGate_DenyNonEphemeralFromToken(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant", `{"max_concurrent":3}`)
	// A token may NEVER create a persistent (non-ephemeral) agent, even with a grant.
	if rr := spawnReq(h, "alpha", `{"type":"claude-code"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("non-ephemeral token add: got %d, want 403", rr.Code)
	}
}

func TestSpawnGate_AllowWithinBudget_DepthCap(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant", `{"max_concurrent":3}`)

	rr := spawnReq(h, "alpha", `{"type":"claude-code","ephemeral":true,"spawned_by":"a1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("in-budget spawn: got %d, want 201 (%s)", rr.Code, rr.Body)
	}
	var sub AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &sub)
	if !sub.Ephemeral || sub.SpawnedBy != "a1" {
		t.Fatalf("spawned agent missing ephemeral/lineage: %+v", sub)
	}
	// Depth cap 1: that ephemeral sub-agent cannot itself spawn.
	if rr := spawnReq(h, "alpha", `{"type":"claude-code","ephemeral":true,"spawned_by":"`+sub.ID+`"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("recursion (spawn by an ephemeral agent) should be 403, got %d", rr.Code)
	}
}

func TestSpawnGate_OperatorAddUnaffected(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	// Operator add (no token context, no grant) still works and isn't ephemeral.
	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/agents", `{"type":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("operator add: got %d, want 201", rr.Code)
	}
	var a AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &a)
	if a.Ephemeral {
		t.Error("operator-created agent must not be ephemeral")
	}
}

// TestSpawnGate_ConcurrentBudgetIsRaceFree is a3's load-bearing requirement: a
// BURST of concurrent spawns must not TOCTOU past max_concurrent. The atomic
// check-and-create under the per-island projectLock must admit EXACTLY the budget.
func TestSpawnGate_ConcurrentBudgetIsRaceFree(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	const budget = 3
	do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant", `{"max_concurrent":3}`)

	const burst = 16
	var wg sync.WaitGroup
	codes := make([]int, burst)
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = spawnReq(h, "alpha", `{"type":"claude-code","ephemeral":true,"spawned_by":"a1"}`).Code
		}(i)
	}
	wg.Wait()

	created := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
		}
	}
	if created != budget {
		t.Fatalf("race: %d spawns admitted, want exactly %d (TOCTOU past the cap)", created, budget)
	}
	if live := ephemeralCount(t, h, "alpha"); live != budget {
		t.Fatalf("roster has %d ephemeral agents, want %d", live, budget)
	}
}
