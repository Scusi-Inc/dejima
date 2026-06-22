package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/aoos/dejima/internal/mailbox"
	"github.com/aoos/dejima/internal/project"
)

// MailboxSendRequest is the body of POST /v1/islands/{name}/mailbox. `from` is
// the sender agent id (self-reported — same-island agents are one trust domain);
// `to` empty broadcasts to the island.
type MailboxSendRequest struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Payload string `json:"payload"`
}

// MailboxPollResponse is the body of GET /v1/islands/{name}/mailbox: the visible
// messages after the requested cursor, plus the island's latest seq (a cursor to
// poll from next).
type MailboxPollResponse struct {
	Messages []mailbox.Message `json:"messages"`
	Latest   int64             `json:"latest"`
}

// sendMailbox posts a message into an island's intra-island mailbox. Reachable
// by the island's own in-island token (accessOwnIsland) and by the operator —
// never by another island, so this stays within one trust domain (cross-island
// exchange is the separate, brokered "link" layer).
func (s *Server) sendMailbox(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req MailboxSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Payload == "" {
		writeError(w, http.StatusBadRequest, errors.New("payload is required"))
		return
	}
	msg := s.mailbox.Send(name, req.From, req.To, req.Topic, req.Payload)
	writeJSON(w, http.StatusCreated, msg)
}

// pollMailbox returns the messages in an island's mailbox visible to ?agent=
// (broadcasts + those addressed to it) with seq > ?since=. Same access scope as
// sendMailbox.
func (s *Server) pollMailbox(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	agent := r.URL.Query().Get("agent")
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		since, _ = strconv.ParseInt(v, 10, 64)
	}
	writeJSON(w, http.StatusOK, MailboxPollResponse{
		Messages: s.mailbox.Poll(name, agent, since),
		Latest:   s.mailbox.Latest(name),
	})
}
