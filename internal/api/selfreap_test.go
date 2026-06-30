package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// tokenDelete drives DELETE /agents/{id} AS AN IN-ISLAND TOKEN caller (the
// self-reap path) by injecting the token-island on the context, mirroring
// spawnReq.
func tokenDelete(h http.Handler, island, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/v1/islands/"+island+"/agents/"+id, nil)
	r = r.WithContext(context.WithValue(r.Context(), tokenIslandKey{}, island))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func spawnEphemeral(t *testing.T, h http.Handler, island string) string {
	t.Helper()
	rr := spawnReq(h, island, `{"type":"claude-code","ephemeral":true,"spawned_by":"p1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("spawn: got %d, want 201 (%s)", rr.Code, rr.Body)
	}
	var a AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &a)
	return a.ID
}

// TestSelfReap_TokenRemovesOwnEphemeral: an island token may DELETE an ephemeral
// sub-agent in its own island.
func TestSelfReap_TokenRemovesOwnEphemeral(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant", `{"max_concurrent":3}`)
	id := spawnEphemeral(t, h, "alpha")

	if rr := tokenDelete(h, "alpha", id); rr.Code != http.StatusNoContent {
		t.Fatalf("self-reap own ephemeral: got %d, want 204 (%s)", rr.Code, rr.Body)
	}
	// Gone from the roster.
	rr := do(t, h, http.MethodGet, "/v1/islands/alpha", "")
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	for _, a := range info.Agents {
		if a.ID == id {
			t.Fatalf("ephemeral %s still present after self-reap", id)
		}
	}
}

// TestSelfReap_TokenCannotRemoveNonEphemeral: a token may NOT remove the primary
// or any non-ephemeral agent — only ephemeral sub-agents.
func TestSelfReap_TokenCannotRemoveNonEphemeral(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	// Add a second, persistent (non-ephemeral) agent via the operator path.
	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/agents", `{"type":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add peer: got %d (%s)", rr.Code, rr.Body)
	}
	var peer AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &peer)

	// Token caller is forbidden from removing the non-ephemeral peer...
	if rr := tokenDelete(h, "alpha", peer.ID); rr.Code != http.StatusForbidden {
		t.Errorf("token removing non-ephemeral peer: got %d, want 403", rr.Code)
	}
	// ...and the primary (the island's first agent).
	rr = do(t, h, http.MethodGet, "/v1/islands/alpha", "")
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if len(info.Agents) == 0 {
		t.Fatal("island has no agents")
	}
	if rr := tokenDelete(h, "alpha", info.Agents[0].ID); rr.Code != http.StatusForbidden {
		t.Errorf("token removing primary %s: got %d, want 403", info.Agents[0].ID, rr.Code)
	}
}

// TestSelfReap_OperatorUnaffected: the operator path (no token island) can still
// remove an ephemeral sub-agent — the gate is token-only.
func TestSelfReap_OperatorUnaffected(t *testing.T) {
	h, _ := newTestServer(t)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	do(t, h, http.MethodPost, "/v1/islands/alpha/spawn-grant", `{"max_concurrent":3}`)
	id := spawnEphemeral(t, h, "alpha")

	if rr := do(t, h, http.MethodDelete, "/v1/islands/alpha/agents/"+id, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("operator remove ephemeral: got %d, want 204 (%s)", rr.Code, rr.Body)
	}
}
