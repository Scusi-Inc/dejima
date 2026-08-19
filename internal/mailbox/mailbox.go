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
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Message is one intra-island message.
type Message struct {
	Seq    int64  `json:"seq"`
	Island string `json:"island"`
	From   string `json:"from"` // sender agent id (literal)
	// FromLabel / ToLabel are the human names for From / To, daemon-resolved from
	// the island roster when a message is returned (read time, so a rename
	// reflects). Empty for a broadcast (no To), an unknown/cross-island handle, or
	// an unlabeled agent. Storage keeps only the ids; these are filled on the way
	// out so consumers render names without a second roster fetch.
	FromLabel string    `json:"from_label,omitempty"`
	To        string    `json:"to,omitempty"` // recipient agent id; empty = broadcast to the island
	ToLabel   string    `json:"to_label,omitempty"`
	Topic     string    `json:"topic,omitempty"` // optional channel within the island
	Payload   string    `json:"payload"`
	Time      time.Time `json:"time"`
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
	// FromLabel is the sender agent's display label, stamped by the daemon at
	// send time from the SOURCE island's roster. A receiving island can't resolve
	// another island's roster (containment), so this is the only way it can show
	// a sender name instead of a bare id. Display-only + omitempty: absent when
	// the sender has no label, and consumers fall back to From (the id). Like the
	// rest of Origin it's unforgeable — only the cross-island delivery path sets it.
	FromLabel string `json:"from_label,omitempty"`
}

// Store is a per-island ring of recent messages. The ring bounds memory so a
// chatty island can't grow it unbounded. When opened with a path (see Open) the
// store also persists every append to disk so undelivered messages AND the seq
// cursor survive a daemon restart — without that, a restart wiped the ring and
// reset seq to 1, silently dropping unread cross-session coordination (the
// no-lost-work bar). NewStore keeps the pure in-memory form for tests.
type Store struct {
	mu      sync.Mutex
	seq     int64
	max     int
	byIsl   map[string][]Message
	now     func() time.Time // injectable for tests
	arrival func(m Message)  // optional: fired (async) after each delivery, for wake-on-message
	path    string           // "" = in-memory only (no persistence)
	log     *slog.Logger     // optional, for persist warnings

	// hooks counts arrival hooks that have been started and not yet returned.
	// The daemon never waits on it — a hook exists precisely so the sender
	// doesn't. Tests do: see WaitArrivalHooks.
	hooks sync.WaitGroup
}

// NewStore returns an in-memory store retaining up to maxPerIsland messages per
// island. Nothing is persisted — use Open for a restart-durable store.
func NewStore(maxPerIsland int) *Store {
	if maxPerIsland <= 0 {
		maxPerIsland = 256
	}
	return &Store{max: maxPerIsland, byIsl: map[string][]Message{}, now: time.Now}
}

// persisted is the on-disk shape: the global seq counter plus every island's
// retained ring. Persisting seq (not just the messages) is what keeps a client's
// saved `since` cursor valid across a restart.
type persisted struct {
	Seq   int64                `json:"seq"`
	ByIsl map[string][]Message `json:"by_island"`
}

// Open returns a store backed by path: it loads any previously persisted
// messages + seq on startup, and persists every append back to path so they
// survive a daemon restart. A missing file starts empty; a corrupt/unreadable
// one is logged and started empty (the daemon must still come up). A nil logger
// is tolerated. maxPerIsland bounds each island's ring as in NewStore.
func Open(path string, maxPerIsland int, log *slog.Logger) *Store {
	s := NewStore(maxPerIsland)
	s.path = path
	s.log = log
	s.load()
	return s
}

// load reads the persisted state into the store (best-effort).
func (s *Store) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) && s.log != nil {
			s.log.Warn("mailbox: could not read persisted store; starting empty", "path", s.path, "err", err)
		}
		return
	}
	var ps persisted
	if err := json.Unmarshal(b, &ps); err != nil {
		if s.log != nil {
			s.log.Warn("mailbox: persisted store is corrupt; starting empty", "path", s.path, "err", err)
		}
		return
	}
	s.seq = ps.Seq
	if ps.ByIsl != nil {
		s.byIsl = ps.ByIsl
	}
}

// persistLocked writes the store to disk atomically (temp + rename). The caller
// MUST hold s.mu. A no-op for an in-memory store; best-effort otherwise (a write
// failure is logged but never breaks the in-memory delivery that already
// happened).
func (s *Store) persistLocked() {
	if s.path == "" {
		return
	}
	b, err := json.MarshalIndent(persisted{Seq: s.seq, ByIsl: s.byIsl}, "", "  ")
	if err != nil {
		if s.log != nil {
			s.log.Warn("mailbox: marshal failed; state not persisted", "err", err)
		}
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".mailbox-*.tmp")
	if err != nil {
		if s.log != nil {
			s.log.Warn("mailbox: persist failed", "err", err)
		}
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		if s.log != nil {
			s.log.Warn("mailbox: persist write failed", "err", err)
		}
		return
	}
	if err := tmp.Close(); err != nil {
		if s.log != nil {
			s.log.Warn("mailbox: persist close failed", "err", err)
		}
		return
	}
	if err := os.Rename(tmpName, s.path); err != nil && s.log != nil {
		s.log.Warn("mailbox: persist rename failed", "err", err)
	}
}

// SetArrivalHook registers a callback fired (in its own goroutine, so it never
// holds the store lock or blocks the sender) after every message is appended —
// the wake-on-message seam (Lane 5 Phase 3.5). nil disables it.
func (s *Store) SetArrivalHook(fn func(m Message)) {
	s.mu.Lock()
	s.arrival = fn
	s.mu.Unlock()
}

// notify fires the arrival hook off the lock. Caller must NOT hold s.mu.
func (s *Store) notify(m Message) {
	s.mu.Lock()
	fn := s.arrival
	s.mu.Unlock()
	if fn == nil {
		return
	}
	// Add before the go, not inside it: the counter has to be raised by the time
	// Send returns, or a caller that sends and then waits can observe zero and
	// walk away while the hook is still being scheduled.
	s.hooks.Add(1)
	go func() {
		defer s.hooks.Done()
		fn(m)
	}()
}

// WaitArrivalHooks blocks until every arrival hook started so far has returned,
// giving up after d. It reports whether they all finished.
//
// For tests. The hook is deliberately detached — that is the point of it, and
// the daemon outlives any individual delivery — but a test process does not.
// A hook that runs on past the end of its test does filesystem work against
// whatever $HOME has become by then: the next test's t.TempDir (where creating
// a directory during RemoveAll produces "unlinkat: directory not empty") or, if
// no test is running, the developer's real home. Both were happening.
//
// The deadline is not optional, so that a hook which blocks forever surfaces as
// a named failure in the test that started it, rather than as the whole binary
// panicking at the ten-minute mark with the wrong test's name on it. That
// misattribution is the exact shape of bug this method exists to close.
func (s *Store) WaitArrivalHooks(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.hooks.Wait()
		close(done)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Send appends a message to an island's ring and returns it with Seq/Time set.
// from is the sender agent id; to is a recipient agent id, or "" to broadcast to
// every agent in the island.
func (s *Store) Send(island, from, to, topic, payload string) Message {
	s.mu.Lock()
	s.seq++
	m := Message{Seq: s.seq, Island: island, From: from, To: to, Topic: topic, Payload: payload, Time: s.now()}
	q := append(s.byIsl[island], m)
	if len(q) > s.max {
		q = q[len(q)-s.max:] // evict oldest
	}
	s.byIsl[island] = q
	s.persistLocked()
	s.mu.Unlock()
	s.notify(m)
	return m
}

// DeliverExternal appends a message delivered into `island`'s mailbox from
// another island (sourceIsland) over a brokered link, stamping daemon-controlled
// Origin (CrossIsland=true). `from` is the source agent's literal id, `fromLabel`
// its display label resolved by the caller from the source roster (may be ""),
// and `to` the local recipient agent. It is distinct from Send precisely so the
// intra-island path can never set Origin — provenance is the daemon's to assert.
func (s *Store) DeliverExternal(island, sourceIsland, from, fromLabel, to, topic, payload string) Message {
	s.mu.Lock()
	s.seq++
	m := Message{
		Seq: s.seq, Island: island, From: from, To: to, Topic: topic, Payload: payload, Time: s.now(),
		Origin: &Origin{SourceIsland: sourceIsland, CrossIsland: true, FromLabel: fromLabel},
	}
	q := append(s.byIsl[island], m)
	if len(q) > s.max {
		q = q[len(q)-s.max:]
	}
	s.byIsl[island] = q
	s.persistLocked()
	s.mu.Unlock()
	s.notify(m)
	return m
}

// DeliverAction appends a cross-island ACTION delegation into `island`'s mailbox
// (Lane 5 Phase 3): a named, typed operation (actionType/params) the daemon's
// action gate authorized. Like DeliverExternal it stamps Origin; it additionally
// sets the structured Action field. Distinct from Send/DeliverExternal so only
// the gated action path can mark a message as an action.
func (s *Store) DeliverAction(island, sourceIsland, from, fromLabel, to, topic, actionType, params string) Message {
	s.mu.Lock()
	s.seq++
	m := Message{
		Seq: s.seq, Island: island, From: from, To: to, Topic: topic, Time: s.now(),
		Origin: &Origin{SourceIsland: sourceIsland, CrossIsland: true, FromLabel: fromLabel},
		Action: &MessageAction{Type: actionType, Params: params},
	}
	q := append(s.byIsl[island], m)
	if len(q) > s.max {
		q = q[len(q)-s.max:]
	}
	s.byIsl[island] = q
	s.persistLocked()
	s.mu.Unlock()
	s.notify(m)
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
