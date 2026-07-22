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
