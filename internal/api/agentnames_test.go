package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/mailbox"
)

// setupTwoAgents creates island "alpha" (primary, auto-labeled) plus a second
// agent labeled "worker", returning both ids.
func setupTwoAgents(t *testing.T, h http.Handler) (primaryID, workerID, primaryLabel string) {
	t.Helper()
	rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body)
	}
	rr = do(t, h, http.MethodPost, "/v1/islands/alpha/agents", `{"type":"claude-code","label":"worker"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add worker: %d %s", rr.Code, rr.Body)
	}
	var worker AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &worker)
	// Primary id + label from the roster.
	rr = do(t, h, http.MethodGet, "/v1/islands/alpha", "")
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	return info.Agents[0].ID, worker.ID, info.Agents[0].Label
}

// TestMailboxCarriesNames: a polled/sent message carries from_label + to_label
// so a consumer renders names without a second roster fetch.
func TestMailboxCarriesNames(t *testing.T) {
	h, _ := newTestServer(t)
	primary, workerID, primaryLabel := setupTwoAgents(t, h)

	// Address the recipient by LABEL ("worker") — resolves to its id server-side.
	body := `{"from":"` + primary + `","to":"worker","payload":"hi"}`
	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/mailbox", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", rr.Code, rr.Body)
	}
	var sent MailboxSendResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &sent)
	if sent.FromLabel != primaryLabel {
		t.Errorf("send from_label = %q, want %q", sent.FromLabel, primaryLabel)
	}
	if sent.ToLabel != "worker" {
		t.Errorf("send to_label = %q, want worker", sent.ToLabel)
	}

	// And on poll (as the recipient, so the directed message is visible).
	rr = do(t, h, http.MethodGet, "/v1/islands/alpha/mailbox?agent="+workerID, "")
	var poll struct {
		Messages []mailbox.Message `json:"messages"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &poll)
	if len(poll.Messages) == 0 {
		t.Fatal("no messages polled")
	}
	m := poll.Messages[len(poll.Messages)-1]
	if m.FromLabel != primaryLabel || m.ToLabel != "worker" {
		t.Errorf("poll labels = from %q / to %q, want %q / worker", m.FromLabel, m.ToLabel, primaryLabel)
	}
}

// TestSpawnedByLabel: AgentInfo carries spawned_by_label so lineage renders as a
// name.
func TestSpawnedByLabel(t *testing.T) {
	h, _ := newTestServer(t)
	primary, _, primaryLabel := setupTwoAgents(t, h)

	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/agents",
		`{"type":"claude-code","ephemeral":true,"spawned_by":"`+primary+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("spawn: %d %s", rr.Code, rr.Body)
	}
	var sub AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &sub)
	if sub.SpawnedByLabel != primaryLabel {
		t.Errorf("create spawned_by_label = %q, want %q", sub.SpawnedByLabel, primaryLabel)
	}

	// And on the roster GET (built from the reloaded project).
	rr = do(t, h, http.MethodGet, "/v1/islands/alpha", "")
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	found := false
	for _, a := range info.Agents {
		if a.ID == sub.ID {
			found = true
			if a.SpawnedByLabel != primaryLabel {
				t.Errorf("roster spawned_by_label = %q, want %q", a.SpawnedByLabel, primaryLabel)
			}
		}
	}
	if !found {
		t.Fatalf("spawned agent %s not in roster", sub.ID)
	}
}

// TestEventCarriesAgentLabel: every event carrying an agent id is enriched with
// agent_label at emit, so subscribers see a name.
func TestEventCarriesAgentLabel(t *testing.T) {
	h, _ := newTestServer(t)
	_, workerID, _ := setupTwoAgents(t, h)

	rr := do(t, h, http.MethodGet, "/v1/islands/alpha/events", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("events: %d %s", rr.Code, rr.Body)
	}
	var evs []events.Event
	_ = json.Unmarshal(rr.Body.Bytes(), &evs)
	sawWorker := false
	for _, e := range evs {
		if e.Agent == workerID {
			sawWorker = true
			if e.AgentLabel != "worker" {
				t.Errorf("event %q for %s: agent_label = %q, want worker", e.Type, e.Agent, e.AgentLabel)
			}
		}
	}
	if !sawWorker {
		t.Fatalf("no event carrying the worker agent id %s found in %d events", workerID, len(evs))
	}
}
