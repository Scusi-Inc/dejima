package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/mailbox"
)

// TestLinkGateDeliversIntoMailbox exercises the Phase 2 broker: cross-island is
// deny-all, a grant is directional, a granted message lands in the target agent's
// ORDINARY mailbox stamped with cross-island Origin, and the reverse is denied.
func TestLinkGateDeliversIntoMailbox(t *testing.T) {
	h, _ := newTestServer(t)
	for _, n := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPost, "/v1/islands",
			`{"repo":"r","name":"`+n+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: got %d", n, rr.Code)
		}
	}
	betaAgent := primaryAgentID(t, h, "beta")

	// Deny-all: with no grant, alpha→beta is refused.
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/send",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"t","payload":"hi"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("ungranted send: got %d, want 403", rr.Code)
	}

	// Operator grants alpha→beta on topic t.
	if rr := do(t, h, http.MethodPost, "/v1/links",
		`{"from":"alpha","to":"beta","topic":"t"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: got %d", rr.Code)
	}

	// A granted send to a NON-existent agent is rejected (never silently vanishes).
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/send",
		`{"to":"beta","to_agent":"ghost","topic":"t","payload":"x"}`); rr.Code != http.StatusNotFound {
		t.Errorf("send to missing agent: got %d, want 404", rr.Code)
	}

	// Granted send to a real agent lands in beta's mailbox, tagged cross-island.
	if rr := do(t, h, http.MethodPost, "/v1/islands/alpha/link/send",
		`{"to":"beta","to_agent":"`+betaAgent+`","topic":"t","payload":"hi beta","from_agent":"a1"}`); rr.Code != http.StatusCreated {
		t.Fatalf("granted send: got %d", rr.Code)
	}
	msgs := pollMailboxAs(t, h, "beta", betaAgent)
	if len(msgs) != 1 {
		t.Fatalf("beta mailbox = %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Payload != "hi beta" || m.From != "a1" {
		t.Errorf("delivered msg = %+v, want payload 'hi beta' from a1", m)
	}
	if m.Origin == nil || !m.Origin.CrossIsland || m.Origin.SourceIsland != "alpha" {
		t.Errorf("Origin = %+v, want CrossIsland from alpha", m.Origin)
	}

	// Directional: the reverse channel is not implied by the forward grant.
	alphaAgent := primaryAgentID(t, h, "alpha")
	if rr := do(t, h, http.MethodPost, "/v1/islands/beta/link/send",
		`{"to":"alpha","to_agent":"`+alphaAgent+`","topic":"t","payload":"reply"}`); rr.Code != http.StatusForbidden {
		t.Errorf("reverse send without its own grant: got %d, want 403", rr.Code)
	}

	// An intra-island send may not forge a reserved link: provenance prefix.
	if rr := do(t, h, http.MethodPost, "/v1/islands/beta/mailbox",
		`{"from":"link:alpha/a1","payload":"spoof"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("spoofed link: From: got %d, want 400", rr.Code)
	}
}

func primaryAgentID(t *testing.T, h http.Handler, island string) string {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+island, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get %s: %d", island, rr.Code)
	}
	var info struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil || len(info.Agents) == 0 {
		t.Fatalf("no agents for %s: %v", island, err)
	}
	return info.Agents[0].ID
}

func pollMailboxAs(t *testing.T, h http.Handler, island, agent string) []mailbox.Message {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+island+"/mailbox?agent="+agent, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("poll mailbox %s: got %d", island, rr.Code)
	}
	var resp MailboxPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode mailbox: %v", err)
	}
	return resp.Messages
}
