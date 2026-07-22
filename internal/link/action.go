package link

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// ActionRequest is a requested cross-island action invocation: source island
// From (agent FromAgent) asking destination island To (agent ToAgent) to run the
// named, typed Action with Params, over the granted channel Topic. It is the
// action-tier analog of a link message — but an action is a NAMED operation B
// exposed, never free text.
type ActionRequest struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`       // source island
	FromAgent string    `json:"from_agent"` // source agent (self-reported within A)
	To        string    `json:"to"`         // destination island
	ToAgent   string    `json:"to_agent"`   // destination agent
	Topic     string    `json:"topic"`      // the granted channel
	Action    string    `json:"action"`     // named, typed action exposed by B
	Params    string    `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Queue holds action requests awaiting operator approval. It is IN-MEMORY by
// design: a daemon restart drops every pending request so nothing ever
// auto-executes after a restart (fail-closed); a TTL expires stale requests
// while the daemon runs. Concurrency-safe.
type Queue struct {
	mu    sync.Mutex
	ttl   time.Duration
	seq   int64
	items map[string]ActionRequest
	now   func() time.Time
}

// NewQueue returns an approval queue whose entries expire after ttl.
func NewQueue(ttl time.Duration) *Queue {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Queue{ttl: ttl, items: map[string]ActionRequest{}, now: time.Now}
}

func (q *Queue) expired(r ActionRequest) bool {
	return q.now().Sub(r.CreatedAt) > q.ttl
}

// Add enqueues a pending request, assigning an ID + CreatedAt, and returns it.
func (q *Queue) Add(r ActionRequest) ActionRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	r.ID = fmt.Sprintf("act-%d", q.seq)
	r.CreatedAt = q.now()
	q.items[r.ID] = r
	return r
}

// Take removes and returns a pending request by id for approval/denial. ok is
// false when the id is unknown OR has expired (an expired request is never
// actionable — fail-closed). Expired entries are pruned on access.
func (q *Queue) Take(id string) (ActionRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.items[id]
	if !ok {
		return ActionRequest{}, false
	}
	delete(q.items, id)
	if q.expired(r) {
		return ActionRequest{}, false
	}
	return r, true
}

// List returns the non-expired pending requests, oldest first, pruning expired
// ones as it goes.
func (q *Queue) List() []ActionRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]ActionRequest, 0, len(q.items))
	for id, r := range q.items {
		if q.expired(r) {
			delete(q.items, id)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
