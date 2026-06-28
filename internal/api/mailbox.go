package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	proj, err := project.Load(name)
	if err != nil {
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
	// A directed message may address the recipient by id OR by label (id wins);
	// resolve a label to the concrete id so the recipient — who polls with its id
	// — actually receives it. Resolved server-side because the mailbox `to` only
	// travels in the request body, so there is no CLI-side API call to resolve
	// against (no new route/role change). The mailbox is intentionally permissive
	// about the target (you may address an id that isn't a live agent — e.g. one
	// that will be added, or a stale handle), so a no-match passes THROUGH
	// unchanged for back-compat; only a genuinely AMBIGUOUS label is rejected,
	// since silently picking one recipient would mis-deliver.
	if to := strings.TrimSpace(req.To); to != "" {
		if id, rerr := project.ResolveAgentRef(proj.Agents, to); rerr == nil {
			req.To = id
		} else if errors.Is(rerr, project.ErrAmbiguousAgent) {
			writeError(w, http.StatusBadRequest, rerr)
			return
		}
	}
	// Belt-and-suspenders: cross-island provenance is the structured Origin field
	// (set only by the daemon's DeliverExternal), but also reserve a "link:"
	// sender prefix so a local agent can't dress an intra-island message up to
	// look like it arrived over a link.
	if strings.HasPrefix(req.From, "link:") {
		writeError(w, http.StatusBadRequest, errors.New(`"from" may not start with the reserved "link:" prefix`))
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
