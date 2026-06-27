// Package egress is the island outbound-network gate — the containment thesis
// applied to the one channel Dejima didn't yet observe or control: an island's
// traffic to the internet (the LLM API, git, package installs, and anything
// else the agent decides to reach). The daemon's existing gates (Port,
// capability, MCP, link) all cover an island reaching back INTO the host or
// other islands; this covers it reaching OUT.
//
// The mechanism is a forward proxy the daemon runs and that islands are pointed
// at via HTTPS_PROXY. It attributes every connection to an island, applies a
// Policy, records the decision, and forwards allowed traffic. This is the
// hostname-level chokepoint — a firewall can later force all egress through it
// (so the policy can't be bypassed), but even alone it gives full visibility and
// cooperative control.
//
// Phase 1 (this package) is observe-first: the default Policy is AllowAll, so
// nothing is blocked — the value is that the operator can finally SEE where each
// island connects. Phase 2 adds real allow/deny policy; the Policy seam is here
// so that lands without reworking the proxy.
package egress

import (
	"sync"
	"time"
)

// Decision is the gate's verdict for one outbound connection.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// Event is one recorded outbound connection attempt from an island. It carries
// the destination by HOST (not full URL/path) and never any payload — egress
// observability is "who did it talk to," not wiretapping its content. For HTTPS
// (a CONNECT tunnel) the host is all the proxy sees anyway.
type Event struct {
	Island   string    `json:"island"`
	Host     string    `json:"host"`           // destination hostname
	Port     string    `json:"port,omitempty"` // destination port ("443", "80", …)
	Method   string    `json:"method"`         // "CONNECT" (HTTPS tunnel) or the HTTP verb
	Decision Decision  `json:"decision"`
	Time     time.Time `json:"time"`
}

// Recorder receives egress events. The daemon plugs in a Log (for the read API)
// and may also fan out to the ledger. A nil Recorder is a valid no-op via
// (*Log)(nil)-style guards at the call sites, but the proxy always holds a
// non-nil one (it defaults to a discard recorder).
type Recorder interface{ Record(Event) }

// discard drops events — the proxy's default so it never nil-panics.
type discard struct{}

func (discard) Record(Event) {}

// Policy decides whether an island may reach a destination host. Phase 1 wires
// AllowAll; Phase 2 replaces it with an operator-grant-backed policy. Keeping it
// an interface is the whole point of shipping the proxy before the policy.
type Policy interface {
	// Allow reports whether island may open a connection to host. Implementations
	// must be safe for concurrent use (the proxy calls it per connection).
	Allow(island, host string) bool
}

// AllowAll is the observe-first Phase-1 policy: log everything, block nothing.
type AllowAll struct{}

// Allow always permits — Phase 1 never blocks.
func (AllowAll) Allow(_, _ string) bool { return true }

// Log is an in-memory, per-island ring of recent egress events. It bounds memory
// (a chatty island can't grow it unbounded) and is the source for the read API.
// It implements Recorder.
type Log struct {
	mu       sync.Mutex
	max      int
	byIsland map[string][]Event
}

// NewLog returns a log retaining up to maxPerIsland events per island.
func NewLog(maxPerIsland int) *Log {
	if maxPerIsland <= 0 {
		maxPerIsland = 256
	}
	return &Log{max: maxPerIsland, byIsland: map[string][]Event{}}
}

// Record appends an event, trimming the island's ring to the cap. Events with an
// empty Island are dropped (unattributed traffic isn't useful and shouldn't
// accumulate under "").
func (l *Log) Record(e Event) {
	if e.Island == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	q := append(l.byIsland[e.Island], e)
	if len(q) > l.max {
		q = q[len(q)-l.max:]
	}
	l.byIsland[e.Island] = q
}

// List returns a copy of the retained events for one island, oldest first.
func (l *Log) List(island string) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	src := l.byIsland[island]
	out := make([]Event, len(src))
	copy(out, src)
	return out
}
