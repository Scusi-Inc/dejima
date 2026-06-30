package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Reproduction for a3 #62: spawned_by + ephemeral come back null from
// GET /agents even though create set ephemeral:true. Asserts the lineage
// survives the create -> save -> reload -> GET round-trip.
func TestLineageSurvivesGet(t *testing.T) {
	h, _ := newTestServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	// Operator creates an ephemeral sub-agent with lineage.
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"claude-code","ephemeral":true,"spawned_by":"a3"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add ephemeral agent: %d %s", rr.Code, rr.Body.String())
	}
	var created AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Logf("CREATE response: ephemeral=%v spawned_by=%q id=%s", created.Ephemeral, created.SpawnedBy, created.ID)
	if !created.Ephemeral || created.SpawnedBy != "a3" {
		t.Fatalf("create response dropped lineage: ephemeral=%v spawned_by=%q", created.Ephemeral, created.SpawnedBy)
	}

	// The bug: GET the roster back (fresh project.Load) and check lineage persists.
	rr = do(t, h, http.MethodGet, "/v1/islands/proj/agents", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get agents: %d %s", rr.Code, rr.Body.String())
	}
	var list []AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var sub *AgentInfo
	for i := range list {
		if list[i].ID == created.ID {
			sub = &list[i]
		}
	}
	if sub == nil {
		t.Fatalf("spawned agent %s not in GET roster", created.ID)
	}
	t.Logf("GET response: ephemeral=%v spawned_by=%q", sub.Ephemeral, sub.SpawnedBy)
	if !sub.Ephemeral {
		t.Errorf("#62: ephemeral came back false from GET (want true)")
	}
	if sub.SpawnedBy != "a3" {
		t.Errorf("#62: spawned_by came back %q from GET (want a3)", sub.SpawnedBy)
	}
}
