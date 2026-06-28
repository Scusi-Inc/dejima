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

// MailboxSendResponse is the body of POST /v1/islands/{name}/mailbox. The
// delivered message is embedded inline, so the JSON stays byte-compatible with
// the old (bare MailboxMessage) response — existing clients that decode a
// mailbox.Message keep working. The added fields are additive + omitempty:
//
//   - UnknownRecipient is true when a DIRECTED send (`to` non-empty) named a
//     recipient that matched NEITHER an id NOR a label in the island's CURRENT
//     roster. Delivery STILL happened (the mailbox is intentionally permissive —
//     you may address a handle that isn't a live agent yet, and the roster can be
//     transiently empty on daemon restart, so a strict reject would false-fail
//     legitimate sends). The CLI surfaces this as a sender-side warning.
//   - Roster is the island's current agents at send time, returned so the CLI can
//     render "delivered anyway, current roster: …" without re-deriving (and re-
//     racing) the roster. Present only alongside UnknownRecipient.
//
// The signal is server-authoritative: the daemon checks against the same roster
// it just resolved the recipient against, so a transient roster gap can't make
// the CLI warn from a roster that disagrees with what was actually checked.
type MailboxSendResponse struct {
	mailbox.Message
	UnknownRecipient bool          `json:"unknown_recipient,omitempty"`
	Roster           []RosterAgent `json:"roster,omitempty"`
}

// RosterAgent is the minimal (id, label) view of a current agent carried in a
// MailboxSendResponse so the sender can name the live agents in its warning.
type RosterAgent struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
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
	var unknownRecipient bool
	if to := strings.TrimSpace(req.To); to != "" {
		if id, rerr := project.ResolveAgentRef(proj.Agents, to); rerr == nil {
			req.To = id
		} else if errors.Is(rerr, project.ErrAmbiguousAgent) {
			writeError(w, http.StatusBadRequest, rerr)
			return
		} else {
			// No id/label match: still deliver to the literal handle (permissive,
			// see above), but flag it so the sender gets a warning listing the
			// current roster. Server-authoritative — derived from the very roster
			// we just resolved against.
			unknownRecipient = true
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
	resp := MailboxSendResponse{Message: msg, UnknownRecipient: unknownRecipient}
	if unknownRecipient {
		resp.Roster = make([]RosterAgent, 0, len(proj.Agents))
		for _, a := range proj.Agents {
			resp.Roster = append(resp.Roster, RosterAgent{ID: a.ID, Label: a.Label})
		}
	}
	writeJSON(w, http.StatusCreated, resp)
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
