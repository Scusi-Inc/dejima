package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/aoos/dejima/internal/mailbox"
)

// TestMailboxEndpoints round-trips the intra-island mailbox API: send a
// broadcast and an addressed message, then poll as an agent (sees broadcasts +
// its own) and with a cursor (only newer).
func TestMailboxEndpoints(t *testing.T) {
	h, _ := newTestServer(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"proj","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d", rr.Code)
	}

	// Mailbox on a missing island is a 404.
	if rr := do(t, h, http.MethodGet, "/v1/islands/nope/mailbox", ""); rr.Code != http.StatusNotFound {
		t.Errorf("poll missing island: got %d, want 404", rr.Code)
	}
	// Empty payload is rejected.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/mailbox", `{"from":"p1"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("empty payload: got %d, want 400", rr.Code)
	}

	// A broadcast (no `to`) and a message addressed to p2.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/mailbox",
		`{"from":"p1","payload":"hello all"}`); rr.Code != http.StatusCreated {
		t.Fatalf("send broadcast: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/mailbox",
		`{"from":"p1","to":"p2","payload":"just p2"}`); rr.Code != http.StatusCreated {
		t.Fatalf("send addressed: got %d", rr.Code)
	}

	// p2 sees both (broadcast + addressed); p3 sees only the broadcast.
	if got := pollMessages(t, h, "proj", "p2", 0); len(got) != 2 {
		t.Errorf("p2 sees %d messages, want 2", len(got))
	}
	p3 := pollMessages(t, h, "proj", "p3", 0)
	if len(p3) != 1 || p3[0].Payload != "hello all" {
		t.Errorf("p3 sees %+v, want just the broadcast", p3)
	}

	// Cursor: polling after the first message's seq yields only the second.
	after := pollMessages(t, h, "proj", "p2", p3[0].Seq)
	if len(after) != 1 || after[0].Payload != "just p2" {
		t.Errorf("p2 after cursor sees %+v, want only 'just p2'", after)
	}
}

// TestMailboxSendByLabel: the mailbox `to` field accepts an agent LABEL, not just
// an id. The daemon resolves it server-side (the only place it can — `to` only
// travels in the request body) so the labelled recipient, polling with its id,
// receives the message. An unknown recipient label is a 400.
func TestMailboxSendByLabel(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"proj","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d", rr.Code)
	}
	// Label the primary "backend" via PATCH.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/proj/agents/p1",
		`{"label":"backend"}`); rr.Code != http.StatusOK {
		t.Fatalf("relabel p1: got %d", rr.Code)
	}

	// Address a message to "backend" (case-insensitively) — must resolve to p1.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/mailbox",
		`{"from":"p2","to":"Backend","payload":"for the backend"}`); rr.Code != http.StatusCreated {
		t.Fatalf("send to label: got %d", rr.Code)
	}
	// p1 (the labelled agent) receives it; an unrelated agent does not.
	if got := pollMessages(t, h, "proj", "p1", 0); len(got) != 1 || got[0].Payload != "for the backend" {
		t.Errorf("p1 should receive the label-addressed message, got %+v", got)
	}
	if got := pollMessages(t, h, "proj", "p9", 0); len(got) != 0 {
		t.Errorf("unrelated agent should see nothing, got %+v", got)
	}

	// The mailbox is permissive about an unknown target (you may address an id
	// that isn't a live agent yet): a no-match passes THROUGH as a literal id, so
	// it is accepted (201) and delivered to that literal handle.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/mailbox",
		`{"from":"p2","to":"ghost","payload":"x"}`); rr.Code != http.StatusCreated {
		t.Errorf("unknown recipient should pass through: got %d, want 201", rr.Code)
	}
	if got := pollMessages(t, h, "proj", "ghost", 0); len(got) != 1 {
		t.Errorf("literal-id recipient should get its message, got %+v", got)
	}
}

// TestMailboxUnknownRecipientWarning: a DIRECTED send whose `to` matches neither
// an id nor a label is STILL delivered (permissive, 201), but the response flags
// unknown_recipient and returns the current roster so the CLI can warn. A send to
// a known id or label, and a broadcast, leave the flag false/absent.
func TestMailboxUnknownRecipientWarning(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"proj","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d", rr.Code)
	}
	// Give the primary (p1) a label so the roster carries an (id,label) pair and
	// the label-resolution path is exercised.
	if rr := do(t, h, http.MethodPatch, "/v1/islands/proj/agents/p1",
		`{"label":"backend"}`); rr.Code != http.StatusOK {
		t.Fatalf("relabel p1: got %d", rr.Code)
	}

	// Unknown recipient: delivered (201) AND flagged, with the roster populated.
	resp := sendMsg(t, h, "proj", `{"from":"p2","to":"ghost","payload":"x"}`)
	if !resp.UnknownRecipient {
		t.Errorf("unknown recipient: unknown_recipient = false, want true")
	}
	if resp.To != "ghost" {
		t.Errorf("unknown recipient: to = %q, want the literal handle %q", resp.To, "ghost")
	}
	var sawBackend bool
	for _, a := range resp.Roster {
		if a.ID == "p1" && a.Label == "backend" {
			sawBackend = true
		}
	}
	if !sawBackend {
		t.Errorf("unknown recipient: roster %+v missing p1/backend", resp.Roster)
	}
	// It really was delivered to the literal handle.
	if got := pollMessages(t, h, "proj", "ghost", 0); len(got) != 1 {
		t.Errorf("literal-id recipient should get its message, got %+v", got)
	}

	// Known recipient by id → not flagged, no roster.
	if r := sendMsg(t, h, "proj", `{"from":"p2","to":"p1","payload":"y"}`); r.UnknownRecipient || len(r.Roster) != 0 {
		t.Errorf("known id: unknown_recipient=%v roster=%+v, want false/empty", r.UnknownRecipient, r.Roster)
	}
	// Known recipient by label → not flagged.
	if r := sendMsg(t, h, "proj", `{"from":"p2","to":"backend","payload":"z"}`); r.UnknownRecipient {
		t.Errorf("known label: unknown_recipient = true, want false")
	}
	// Broadcast (no `to`) → unaffected.
	if r := sendMsg(t, h, "proj", `{"from":"p2","payload":"all"}`); r.UnknownRecipient || len(r.Roster) != 0 {
		t.Errorf("broadcast: unknown_recipient=%v roster=%+v, want false/empty", r.UnknownRecipient, r.Roster)
	}
}

// sendMsg POSTs a mailbox send, asserts 201, and decodes the MailboxSendResponse.
func sendMsg(t *testing.T, h http.Handler, island, body string) MailboxSendResponse {
	t.Helper()
	rr := do(t, h, http.MethodPost, "/v1/islands/"+island+"/mailbox", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("send %s: got %d, want 201", body, rr.Code)
	}
	var resp MailboxSendResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	return resp
}

func pollMessages(t *testing.T, h http.Handler, island, agent string, since int64) []mailbox.Message {
	t.Helper()
	path := "/v1/islands/" + island + "/mailbox?agent=" + agent
	if since > 0 {
		path += "&since=" + strconv.FormatInt(since, 10)
	}
	rr := do(t, h, http.MethodGet, path, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("poll: got %d", rr.Code)
	}
	var resp MailboxPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	return resp.Messages
}
