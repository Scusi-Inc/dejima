// Package mailbox is the intra-island agent message store — Lane 5, Phase 1 of
// docs/inter-island-exchange-spec.md. Agents in the SAME island exchange small
// typed messages through the daemon (a shared blackboard / mailbox). It is the
// low-risk layer: same-island agents are one trust domain (they already share
// /workspace + home), so intra-island messaging is allowed by default.
//
// Cross-island exchange is deliberately NOT here — that is the brokered,
// operator-granted, audited "link" layer (Phase 2+), with a separate deny-all
// posture and an action-delegation gate. Keeping the two apart is the whole point.
package mailbox

import (
	"sync"
	"time"
)

// Message is one intra-island message.
type Message struct {
	Seq     int64     `json:"seq"`
	Island  string    `json:"island"`
	From    string    `json:"from"`            // sender agent id (literal)
	To      string    `json:"to,omitempty"`    // recipient agent id; empty = broadcast to the island
	Topic   string    `json:"topic,omitempty"` // optional channel within the island
	Payload string    `json:"payload"`
	Time    time.Time `json:"time"`
	// Origin is daemon-stamped provenance, set ONLY for messages delivered from
	// another island over a brokered link (Lane 5). nil for ordinary intra-island
	// messages. Agents cannot set it — see DeliverExternal vs Send.
	Origin *Origin `json:"origin,omitempty"`
	// Action, when non-nil, marks this as a cross-island ACTION delegation (Lane 5
	// Phase 3) rather than free-form info: a NAMED, typed operation the recipient
	// island exposed, authorized by the daemon's action gate. Set only by
	// DeliverAction. The recipient runs its handler for Action.Type — it must not
	// interpret Payload as a free-form prompt.
	Action *MessageAction `json:"action,omitempty"`
}

// MessageAction is a named, typed action invocation carried by a cross-island
// action delegation. Type is one of the recipient island's exposed action types.
type MessageAction struct {
	Type   string `json:"type"`
	Params string `json:"params,omitempty"`
}

// Origin marks a message that entered this island's mailbox from ANOTHER island
// over a brokered link. It's machine-read provenance (SDK / activity feed / audit
// tooling branch on CrossIsland) — a structured field rather than a parseable
// sender-string prefix, and unforgeable because only the daemon's cross-island
// delivery path sets it.
type Origin struct {
	SourceIsland string `json:"source_island"`
	CrossIsland  bool   `json:"cross_island"`
}

// Store is an in-memory, per-island ring of recent messages. Intra-island
// coordination is ephemeral by design in Phase 1 — durable mail is a later
// concern; the ring bounds memory so a chatty island can't grow unbounded.
type Store struct {
	mu    sync.Mutex
	seq   int64
	max   int
	byIsl map[string][]Message
	now   func() time.Time // injectable for tests
}

// NewStore returns a store retaining up to maxPerIsland messages per island.
func NewStore(maxPerIsland int) *Store {
	if maxPerIsland <= 0 {
		maxPerIsland = 256
	}
	return &Store{max: maxPerIsland, byIsl: map[string][]Message{}, now: time.Now}
}

// Send appends a message to an island's ring and returns it with Seq/Time set.
// from is the sender agent id; to is a recipient agent id, or "" to broadcast to
// every agent in the island.
func (s *Store) Send(island, from, to, topic, payload string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m := Message{Seq: s.seq, Island: island, From: from, To: to, Topic: topic, Payload: payload, Time: s.now()}
	q := append(s.byIsl[island], m)
	if len(q) > s.max {
		q = q[len(q)-s.max:] // evict oldest
	}
	s.byIsl[island] = q
	return m
}

// DeliverExternal appends a message delivered into `island`'s mailbox from
// another island (sourceIsland) over a brokered link, stamping daemon-controlled
// Origin (CrossIsland=true). `from` is the source agent's literal id and `to` the
// local recipient agent. It is distinct from Send precisely so the intra-island
// path can never set Origin — provenance is the daemon's to assert.
func (s *Store) DeliverExternal(island, sourceIsland, from, to, topic, payload string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m := Message{
		Seq: s.seq, Island: island, From: from, To: to, Topic: topic, Payload: payload, Time: s.now(),
		Origin: &Origin{SourceIsland: sourceIsland, CrossIsland: true},
	}
	q := append(s.byIsl[island], m)
	if len(q) > s.max {
		q = q[len(q)-s.max:]
	}
	s.byIsl[island] = q
	return m
}

// DeliverAction appends a cross-island ACTION delegation into `island`'s mailbox
// (Lane 5 Phase 3): a named, typed operation (actionType/params) the daemon's
// action gate authorized. Like DeliverExternal it stamps Origin; it additionally
// sets the structured Action field. Distinct from Send/DeliverExternal so only
// the gated action path can mark a message as an action.
func (s *Store) DeliverAction(island, sourceIsland, from, to, topic, actionType, params string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m := Message{
		Seq: s.seq, Island: island, From: from, To: to, Topic: topic, Time: s.now(),
		Origin: &Origin{SourceIsland: sourceIsland, CrossIsland: true},
		Action: &MessageAction{Type: actionType, Params: params},
	}
	q := append(s.byIsl[island], m)
	if len(q) > s.max {
		q = q[len(q)-s.max:]
	}
	s.byIsl[island] = q
	return m
}

// Poll returns the retained messages in island visible to agent `agent` with
// Seq > since, ordered by Seq. Visible = broadcasts (To == "") plus messages
// addressed To == agent. since == 0 returns all retained. An empty agent sees
// only broadcasts (an operator/observer view).
func (s *Store) Poll(island, agent string, since int64) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.byIsl[island] {
		if m.Seq <= since {
			continue
		}
		if m.To == "" || (agent != "" && m.To == agent) {
			out = append(out, m)
		}
	}
	return out
}

// Latest returns the highest seq retained for an island (0 if none) — a cheap
// cursor for "everything after now".
func (s *Store) Latest(island string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.byIsl[island]
	if len(q) == 0 {
		return 0
	}
	return q[len(q)-1].Seq
}
