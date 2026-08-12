package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/mailbox"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Wake-on-message (Lane 5, Phase 3.5) — the actor-runtime twin of idle
// auto-hibernate. Delivery (mailbox), authorization (link), and audit (ledger)
// are done; this turns "stored + authorized" into "the idle recipient actually
// looks." The MECHANISM (reaching into a contained session, waking a hibernated
// island) can only live in the daemon; the POLICY ships a sane default and is
// overridable by a wrapper via the mailbox.arrival event.
//
// Default soft-notify (info tier): a BATCHED POINTER ("📬 N new — dejima msg
// poll"), never a payload dump, delivered only at a TURN BOUNDARY (never
// mid-turn), de-duped to one nudge per quiet period. A HARD interrupt is NOT a
// flag here — it is an exposed action routed through the Phase-3 gate (deny-all +
// approval + audited), so it can't be a DoS vector.

// nudgeKey identifies a pending soft-notify for one recipient agent.
type nudgeKey struct{ island, agent string }

// wakeNotifier batches unseen-message counts per recipient until they can be
// flushed at a turn boundary. Concurrency-safe; pure (no I/O) so it unit-tests
// without a Server.
type wakeNotifier struct {
	mu        sync.Mutex
	pending   map[nudgeKey]int
	firstSeen map[nudgeKey]time.Time // when each key's oldest undelivered nudge arrived
}

func newWakeNotifier() *wakeNotifier {
	return &wakeNotifier{pending: map[nudgeKey]int{}, firstSeen: map[nudgeKey]time.Time{}}
}

// add records one more unseen message for (island, agent) at time now. De-dupe is
// implicit: many arrivals accumulate into a single count, flushed as one nudge.
// firstSeen stamps the oldest pending arrival so flushNudges can tell a nudge
// that's merely waiting for a turn boundary from one that's stuck (recipient
// never reports a boundary — e.g. a stale shim).
func (n *wakeNotifier) add(island, agent string, now time.Time) {
	n.mu.Lock()
	k := nudgeKey{island, agent}
	n.pending[k]++
	if _, ok := n.firstSeen[k]; !ok {
		n.firstSeen[k] = now
	}
	n.mu.Unlock()
}

// keys returns the recipients with pending nudges (a snapshot).
func (n *wakeNotifier) keys() []nudgeKey {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]nudgeKey, 0, len(n.pending))
	for k := range n.pending {
		out = append(out, k)
	}
	return out
}

// take removes and returns the pending count for a recipient (0 if none) — called
// when a nudge is actually delivered, so the quiet-period counter resets.
func (n *wakeNotifier) take(k nudgeKey) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.pending[k]
	delete(n.pending, k)
	delete(n.firstSeen, k)
	return c
}

// pendingSince returns when the oldest undelivered nudge for k arrived (and
// whether one is pending).
func (n *wakeNotifier) pendingSince(k nudgeKey) (time.Time, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.firstSeen[k]
	return t, ok
}

// onMailboxArrival is the store's arrival hook. It always emits the
// mailbox.arrival event (the app-layer override seam), then — when soft-notify is
// enabled — records a nudge, wakes a hibernated recipient island, and tries to
// flush at once. Runs in its own goroutine (the store calls it async).
func (s *Server) onMailboxArrival(m mailbox.Message) {
	s.emit(events.Event{
		Type:   events.TypeMailboxArrival,
		Island: m.Island,
		Agent:  m.To,
		Payload: map[string]any{
			"from":         m.From,
			"cross_island": m.Origin != nil && m.Origin.CrossIsland,
			"action":       m.Action != nil,
		},
	})
	if !s.wakeEnabled {
		return
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.wakeIslandFor(ctx, m.Island) // an idle agent in a hibernated island can't be nudged until it's up
	if m.To != "" {
		s.wakeNudges.add(m.Island, m.To, now)
	} else {
		// Broadcast (no single To): mirror mailbox.Poll, where a broadcast is
		// visible to EVERY agent. A directed message nudges only its recipient;
		// a broadcast must nudge each agent (minus the sender, who already knows),
		// or a broadcast is silently unseen until each agent happens to poll.
		s.addBroadcastNudges(m.Island, m.From, now)
	}
	s.flushNudges(ctx)
}

// addBroadcastNudges queues one nudge per agent in the island for a broadcast
// message, skipping the sender. Mirrors mailbox.Poll's broadcast visibility (a
// To=="" message reaches all agents). Best-effort: an island that can't be
// loaded yields no nudges (the arrival event already fired for wrapper policy).
func (s *Server) addBroadcastNudges(island, sender string, now time.Time) {
	p, err := project.Load(island)
	if err != nil {
		return
	}
	for i := range p.Agents {
		if id := p.Agents[i].ID; id != "" && id != sender {
			s.wakeNudges.add(island, id, now)
		}
	}
}

const (
	// wakeStuckGrace is how long a nudge waits for an idle turn-boundary before we
	// deliver it best-effort. Past this, an undelivered nudge usually means the
	// recipient never reports a boundary — e.g. a stale in-island shim that can't
	// POST agent-state (version skew) — so holding forever just loses mail.
	wakeStuckGrace = 2 * time.Minute
	// wakeHeartbeatStale: an agent-state older than this (or absent) means the
	// agent isn't actively turning, so a best-effort nudge won't clobber live work.
	wakeHeartbeatStale = 90 * time.Second
	// islandDejimaBin is the canonical in-island CLI path (image/Dockerfile installs
	// it here). The nudge references it by ABSOLUTE path so a broken PATH or a stale
	// shim shadowing `dejima` doesn't swallow the poll command the agent runs. A
	// genuinely absent binary — an island built from a pre-CLI image — is the
	// version-skew remedy (`dejima doctor` / rebuild), surfaced elsewhere, not here.
	islandDejimaBin = "/usr/local/bin/dejima"
)

// flushNudges delivers each pending nudge whose recipient is at a turn boundary
// (idle), leaving the rest queued for the ticker. Never injects mid-turn — except
// a nudge stuck past wakeStuckGrace to an agent with no fresh heartbeat is
// delivered best-effort (+ warned), so a skewed/silent recipient still learns it
// has mail instead of missing it forever.
func (s *Server) flushNudges(ctx context.Context) {
	now := time.Now()
	for _, k := range s.wakeNudges.keys() {
		if !s.idleFn(k.island, k.agent) {
			since, ok := s.wakeNudges.pendingSince(k)
			if !ok || now.Sub(since) < wakeStuckGrace || s.agentHasFreshHeartbeat(k.island, k.agent, now) {
				continue // genuinely busy, or not stuck yet — hold; the ticker retries
			}
			s.log.Warn("wake-on-message: no idle heartbeat — delivering best-effort (possible stale island shim; run `dejima upgrade`)",
				"island", k.island, "agent", k.agent)
		}
		n := s.wakeNudges.take(k)
		if n == 0 {
			continue
		}
		p, err := project.Load(k.island)
		if err != nil {
			continue
		}
		a, ok := p.AgentByID(k.agent)
		if !ok {
			continue
		}
		text := fmt.Sprintf("📬 %d new message(s) — run: %s msg poll", n, islandDejimaBin)
		if err := s.injectFn(ctx, p, a, text); err != nil {
			s.log.Debug("wake inject", "island", k.island, "agent", k.agent, "err", err)
		}
	}
}

// agentHasFreshHeartbeat reports whether the agent posted state within
// wakeHeartbeatStale of now — i.e. it's actively reporting, so a non-idle state is
// real work to protect rather than a skewed/silent shim. Absent/zero/old state →
// false (treat as not-actively-turning).
func (s *Server) agentHasFreshHeartbeat(island, agent string, now time.Time) bool {
	st := s.agentStateOf(island, agent)
	return st != nil && !st.UpdatedAt.IsZero() && now.Sub(st.UpdatedAt) < wakeHeartbeatStale
}

// agentIdleAtBoundary reports whether an agent is at a turn boundary — safe to
// inject without clobbering mid-turn work. It reuses the agent-liveness heartbeat
// (the idle-seconds path): the last agent-state being "waiting-for-input" (paused
// at the prompt) or "task-complete" (just finished a turn). Conservative: an
// agent with no heartbeat is treated as NOT at a boundary (it relies on polling),
// because injecting into an unknown state risks interrupting work.
func (s *Server) agentIdleAtBoundary(island, agent string) bool {
	st := s.agentStateOf(island, agent)
	if st == nil {
		return false
	}
	return st.Latest == "waiting-for-input" || st.Latest == "task-complete"
}

// tmuxInject is the default per-adapter injection seam: a PTY write into the
// agent's tmux session, so an idle prompt receives the nudge as its next input.
// Headless agents (no tmux session — e.g. OpenClaw, which polls its own loop)
// are skipped; they pick up mail on their next poll. A wrapper or a richer
// per-adapter shim can replace s.injectFn.
func (s *Server) tmuxInject(ctx context.Context, p *project.Project, a *project.AgentSpec, text string) error {
	if a.Tmux == "" {
		return nil // headless / no attachable session — relies on polling
	}
	_, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"tmux", "send-keys", "-t", a.Tmux, text, "Enter"})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("tmux send-keys exit %d", code)
	}
	return nil
}

// wakeIslandFor wakes a hibernated/stopped island so a queued nudge can reach it
// — the wake half of the actor model (paired with idle auto-hibernate). No-op when
// the island is already running. Bounded + best-effort; respects the per-island
// lock so it can't race a concurrent lifecycle op.
func (s *Server) wakeIslandFor(ctx context.Context, name string) {
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()
	p, err := project.Load(name)
	if err != nil {
		return
	}
	status, err := s.rt.Status(ctx, p.ContainerName())
	if err != nil || status == runtime.StatusRunning {
		return
	}
	switch status {
	case runtime.StatusMissing:
		if err := s.createContainerForProject(ctx, p, "", false); err != nil {
			s.log.Warn("wake-on-message: recreate", "island", name, "err", err)
			return
		}
	default:
		if err := s.rt.StartContainer(ctx, p.ContainerName()); err != nil {
			s.log.Warn("wake-on-message: start", "island", name, "err", err)
			return
		}
	}
	p.DesiredState = project.StateRunning
	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		return
	}
	s.emit(events.Event{Type: events.TypeIslandWoken, Island: name, Payload: map[string]any{"reason": "message"}})
}

// DefaultWakeFlushInterval bounds how often queued nudges are retried for
// delivery (a busy agent's nudge waits here until it next hits a turn boundary).
// It's the fallback when RunWakeNotifier is given a non-positive interval; the
// daemon exposes the live value via --wake-flush-interval / DEJIMAD_WAKE_FLUSH_INTERVAL.
const DefaultWakeFlushInterval = 15 * time.Second

// RunWakeNotifier periodically flushes queued nudges so a message that arrived
// while an agent was busy is delivered once it reaches a turn boundary. interval
// sets the retry cadence; <= 0 falls back to DefaultWakeFlushInterval. Run it in
// its own goroutine; returns when ctx is cancelled. A no-op loop when soft-notify
// is disabled.
func (s *Server) RunWakeNotifier(ctx context.Context, interval time.Duration) {
	if !s.wakeEnabled {
		return
	}
	if interval <= 0 {
		interval = DefaultWakeFlushInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			s.flushNudges(fctx)
			cancel()
		}
	}
}
