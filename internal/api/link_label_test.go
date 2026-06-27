package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// setAgentLabel sets a display label on an island's agent, on disk, the way a
// relabel would — so the daemon's send-time roster lookup resolves it.
func setAgentLabel(t *testing.T, island, agentID, label string) {
	t.Helper()
	p, err := project.Load(island)
	if err != nil {
		t.Fatalf("load %s: %v", island, err)
	}
	p.EnsureAgents()
	found := false
	for i := range p.Agents {
		if p.Agents[i].ID == agentID {
			p.Agents[i].Label = label
			found = true
		}
	}
	if !found {
		t.Fatalf("island %s has no agent %s to label", island, agentID)
	}
	if err := p.Save(); err != nil {
		t.Fatalf("save %s: %v", island, err)
	}
}

// TestLinkActionStampsDisplayLabels verifies the daemon stamps sender/recipient
// display labels at send time onto the queued ActionRequest (from_label/to_label)
// and onto the delivered message's cross-island Origin (from_label) — the
// agent-facing half of "show names, not ids" that a receiving island can't
// resolve itself (containment).
func TestLinkActionStampsDisplayLabels(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: got %d", n, rr.Code)
		}
	}
	betaAgent := primaryAgentID(t, h, "beta")
	setAgentLabel(t, "alpha", "a1", "frontend")
	setAgentLabel(t, "beta", betaAgent, "backend")

	if rr := do(t, h, http.MethodPost, "/v1/links",
		`{"from":"alpha","to":"beta","topic":"ops"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPut, "/v1/islands/beta/link/actions/deploy", ""); rr.Code != http.StatusOK {
		t.Fatalf("expose: got %d", rr.Code)
	}

	// Send → queued. The queued ActionRequest must carry both display labels.
	rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/action",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"ops","action":"deploy","from_agent":"a1"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("queued action: got %d, want 202", rr.Code)
	}

	listRR := do(t, h, http.MethodGet, "/v1/link/actions", "")
	if listRR.Code != http.StatusOK {
		t.Fatalf("list pending: got %d", listRR.Code)
	}
	var pend LinkPendingResponse
	if err := json.Unmarshal(listRR.Body.Bytes(), &pend); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if len(pend.Pending) != 1 {
		t.Fatalf("want 1 pending, got %d", len(pend.Pending))
	}
	ar := pend.Pending[0]
	if ar.FromLabel != "frontend" {
		t.Errorf("from_label = %q, want frontend", ar.FromLabel)
	}
	if ar.ToLabel != "backend" {
		t.Errorf("to_label = %q, want backend", ar.ToLabel)
	}
	// ids stay the addressing handle.
	if ar.FromAgent != "a1" || ar.ToAgent != betaAgent {
		t.Errorf("ids changed: from_agent=%q to_agent=%q", ar.FromAgent, ar.ToAgent)
	}

	// Approve → delivered message's Origin carries the sender's label.
	if rr := do(t, h, http.MethodPost, "/v1/link/actions/"+ar.ID+"/approve", ""); rr.Code != http.StatusOK {
		t.Fatalf("approve: got %d", rr.Code)
	}
	msgs := pollMailboxAs(t, h, "beta", betaAgent)
	if len(msgs) != 1 || msgs[0].Origin == nil {
		t.Fatalf("delivered message/Origin missing: %+v", msgs)
	}
	if msgs[0].Origin.FromLabel != "frontend" {
		t.Errorf("delivered Origin.from_label = %q, want frontend", msgs[0].Origin.FromLabel)
	}
}
