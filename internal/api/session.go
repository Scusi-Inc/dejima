package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/bridge"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// PresenceEntry describes one client attached to an island's session.
type PresenceEntry struct {
	Label    string    `json:"label"`
	JoinedAt time.Time `json:"joined_at"`
}

// ClientHistoryEntry is one row in the recent-clients ring buffer.
type ClientHistoryEntry struct {
	Label      string    `json:"label"`
	Island     string    `json:"island"`
	AttachedAt time.Time `json:"attached_at"`
	DetachedAt time.Time `json:"detached_at,omitempty"`
}

// presenceTracker is the per-island registry of attached clients.
type presenceTracker struct {
	mu      sync.Mutex
	clients map[*presenceHandle]presenceRecord
}

// presenceHandle is the per-attachment map key. It must be NON-zero-size: Go
// allocates every `&struct{}{}` to the same runtime.zerobase address, so a
// zero-size handle would make distinct attaches collide on one map key —
// silently collapsing the presence count (and RevokeAll) to one client per
// agent. The unused byte forces a unique address per handle.
type presenceHandle struct{ _ byte }

// presenceRecord pairs a presence entry with a cancel function so `dejima
// sessions revoke` can forcibly drop the underlying websocket.
type presenceRecord struct {
	Entry  PresenceEntry
	Cancel context.CancelFunc
}

func newPresenceTracker() *presenceTracker {
	return &presenceTracker{clients: map[*presenceHandle]presenceRecord{}}
}

func (p *presenceTracker) Attach(label string, cancel context.CancelFunc) (*presenceHandle, []PresenceEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	others := make([]PresenceEntry, 0, len(p.clients))
	for _, r := range p.clients {
		others = append(others, r.Entry)
	}
	h := &presenceHandle{}
	p.clients[h] = presenceRecord{
		Entry:  PresenceEntry{Label: label, JoinedAt: time.Now().UTC()},
		Cancel: cancel,
	}
	return h, others
}

func (p *presenceTracker) Detach(h *presenceHandle) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, h)
}

func (p *presenceTracker) Snapshot() []PresenceEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PresenceEntry, 0, len(p.clients))
	for _, r := range p.clients {
		out = append(out, r.Entry)
	}
	return out
}

// RevokeAll signals all currently-attached clients to disconnect.
func (p *presenceTracker) RevokeAll() int {
	p.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(p.clients))
	for _, r := range p.clients {
		cancels = append(cancels, r.Cancel)
	}
	count := len(cancels)
	p.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	return count
}

// SessionEnvelope is the JSON framing on the websocket. Three message types:
//   - {"type":"hello","attached":[...]}   server → client on connect
//   - {"type":"data","b64":"..."}         both directions
//   - {"type":"resize","rows":N,"cols":N} client → server
//   - {"type":"presence","attached":[...]} server → client when others join/leave
//
// Term/ColorTerm ride along on the FIRST resize (the client's opening message)
// and are ignored on later ones — they describe the client's terminal, which
// does not change mid-session. They are optional in both directions: an older
// client omits them and an older daemon ignores them, so either side upgrading
// alone is safe. See bridge.TermEnv for what the daemon does with them.
type SessionEnvelope struct {
	Type      string          `json:"type"`
	B64       string          `json:"b64,omitempty"`
	Rows      uint16          `json:"rows,omitempty"`
	Cols      uint16          `json:"cols,omitempty"`
	Term      string          `json:"term,omitempty"`
	ColorTerm string          `json:"colorterm,omitempty"`
	Attached  []PresenceEntry `json:"attached,omitempty"`
}

// presenceKey is the composite map key for an (island, agent) presence tracker.
// The NUL separator can't appear in either component, so the prefix scan in
// islandPresence is unambiguous.
func presenceKey(island, agentID string) string {
	return island + "\x00" + agentID
}

// trackerFor returns (or lazily creates) the presence tracker for one agent.
func (s *Server) trackerFor(island, agentID string) *presenceTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.presence == nil {
		s.presence = map[string]*presenceTracker{}
	}
	key := presenceKey(island, agentID)
	t, ok := s.presence[key]
	if !ok {
		t = newPresenceTracker()
		s.presence[key] = t
	}
	return t
}

// islandPresence returns the union of every agent's attached clients for an
// island (used for the island-level Attached field and overview counts).
func (s *Server) islandPresence(island string) []PresenceEntry {
	prefix := island + "\x00"
	s.mu.Lock()
	trackers := make([]*presenceTracker, 0)
	for k, t := range s.presence {
		if strings.HasPrefix(k, prefix) {
			trackers = append(trackers, t)
		}
	}
	s.mu.Unlock()
	var out []PresenceEntry
	for _, t := range trackers {
		out = append(out, t.Snapshot()...)
	}
	return out
}

// attachedSessions returns every client currently attached to any island's
// terminal session. A daemon self-update restart drops all of them, so the
// update path consults this to defer the restart while terminals are open
// (the cause of "terminal tabs keep closing" during dogfood self-updates).
func (s *Server) attachedSessions() []PresenceEntry {
	s.mu.Lock()
	trackers := make([]*presenceTracker, 0, len(s.presence))
	for _, t := range s.presence {
		trackers = append(trackers, t)
	}
	s.mu.Unlock()
	var out []PresenceEntry
	for _, t := range trackers {
		out = append(out, t.Snapshot()...)
	}
	return out
}

// presenceSnapshot returns the attached clients for one agent without creating a
// tracker (read path).
func (s *Server) presenceSnapshot(island, agentID string) []PresenceEntry {
	s.mu.Lock()
	t, ok := s.presence[presenceKey(island, agentID)]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return t.Snapshot()
}

// statusRestart is the websocket close code the daemon sends every attached
// session on shutdown so the client reconnects through it instead of exiting.
// 1012 (Service Restart) is the semantically correct code; coder/websocket
// exposes it as StatusServiceRestart and permits it on the wire. The client's
// classifySessionClose maps any non-NormalClosure close (this included) to a
// patient reconnect, and the in-island tmux survives the daemon bounce, so the
// terminal resumes after a ~1s blink rather than dropping.
const statusRestart = websocket.StatusServiceRestart

// sessionConnHandle is the per-connection registry key. Like presenceHandle it
// must be NON-zero-size so distinct connections get distinct map keys (Go
// allocates every &struct{}{} to the same address).
type sessionConnHandle struct{ _ byte }

// registerSessionConn adds a live session websocket to the restart registry and
// returns its handle. Every session/terminal websocket (agent sessions, host
// terminals, island shells) registers on Accept and unregisters on close, so
// CloseSessionsForRestart can reach each one. If a restart is already in flight
// when a late connection registers, it is closed immediately with the restart
// code (it would otherwise miss the broadcast and hang until its own teardown).
func (s *Server) registerSessionConn(conn *websocket.Conn) *sessionConnHandle {
	s.restartMu.Lock()
	if s.sessionConns == nil {
		s.sessionConns = map[*sessionConnHandle]*websocket.Conn{}
	}
	restarting := s.restarting
	h := &sessionConnHandle{}
	s.sessionConns[h] = conn
	s.restartMu.Unlock()
	if restarting {
		_ = conn.Close(statusRestart, "dejimad restarting")
	}
	return h
}

// unregisterSessionConn drops a connection from the restart registry (deferred
// on the handler's exit).
func (s *Server) unregisterSessionConn(h *sessionConnHandle) {
	s.restartMu.Lock()
	delete(s.sessionConns, h)
	s.restartMu.Unlock()
}

// restartInProgress reports whether CloseSessionsForRestart has run. The session
// pumps consult it before emitting their normal "deliberate end" signals so a
// daemon shutdown doesn't masquerade as a detach/exit and stop the client.
func (s *Server) restartInProgress() bool {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.restarting
}

// CloseSessionsForRestart marks the daemon as restarting and closes every
// attached session websocket with a reconnect-triggering close (Service Restart,
// 1012). Clients re-dial and resume the still-running in-island tmux, turning a
// daemon self-update/restart into a brief reconnect blink rather than a
// fleet-wide terminal drop. Call it during shutdown BEFORE http.Server.Shutdown
// (which does not close hijacked websockets). Returns the number closed so the
// caller can log it and pause briefly for the closes to flush.
func (s *Server) CloseSessionsForRestart() int {
	s.restartMu.Lock()
	s.restarting = true
	conns := make([]*websocket.Conn, 0, len(s.sessionConns))
	for _, c := range s.sessionConns {
		conns = append(conns, c)
	}
	s.restartMu.Unlock()
	for _, c := range conns {
		_ = c.Close(statusRestart, "dejimad restarting")
	}
	return len(conns)
}

func (s *Server) sessionWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	agentID := r.PathValue("id") // empty for the legacy /session route → primary
	label := r.URL.Query().Get("label")
	if label == "" {
		label = "anonymous"
	}

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// Resolve which agent to attach to: an explicit id, or the primary.
	spec := p.PrimaryAgent()
	if agentID != "" {
		a, ok := p.AgentByID(agentID)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, agentID))
			return
		}
		spec = a
	}
	if spec == nil {
		writeError(w, http.StatusConflict, fmt.Errorf("island %q has no agents to attach to", name))
		return
	}
	if !handlers.Attachable(spec.Type) {
		// Headless agents run a user-supplied command as their main process —
		// there's no tmux to attach to. Surface the right next step.
		writeError(w, http.StatusConflict,
			fmt.Errorf("agent %q in island %q is headless; it has no attach surface — use `dejima logs %s --follow`", spec.ID, name, name))
		return
	}
	tmuxName := spec.Tmux
	if tmuxName == "" {
		tmuxName = "agent-" + spec.ID // defensive: every interactive agent has a session
	}
	status, err := s.rt.Status(r.Context(), p.ContainerName())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status != runtime.StatusRunning {
		writeError(w, http.StatusConflict,
			fmt.Errorf("island %q is %s; wake it first with `dejima wake %s`", name, status, name))
		return
	}

	// Make sure the session exists WITH THE AGENT IN IT before attaching.
	//
	// The attach runs `tmux new-session -A`, which CREATES the session when it is
	// missing — an empty login shell. image/start.sh then guards its own launch
	// with `if ! tmux has-session`, sees the name taken, and never starts the
	// agent at all. So attaching a moment too early does not just show the wrong
	// thing once; it PERMANENTLY replaces the agent with a bare shell, and the
	// island looks up and healthy.
	//
	// The window is small and easy to hit exactly when it matters: recreate the
	// island to apply a secret ([!] in the secrets pane), and the dashboard
	// reattaches while the container is still coming up. Reported from the field
	// as "after the ! restart it ends up at the terminal workspace, not in the
	// agent" — which reads like a cold start and is not one.
	//
	// ensureAgentSession is idempotent and checks for the session first, so when
	// the entrypoint wins the race this costs one `tmux has-session`.
	s.ensureAttachTarget(r.Context(), p, spec, tmuxName)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // local trust boundary; remote auth handled at TCP listener (M4)
	})
	if err != nil {
		s.log.Error("ws accept", "err", err)
		return
	}
	// On a normal end, close cleanly (NormalClosure → the client stops). During a
	// daemon restart, CloseSessionsForRestart has already closed this conn with
	// the Service-Restart code and set restarting, so this deferred NormalClosure
	// is a no-op (the first close wins) — and we also skip it explicitly to keep
	// intent obvious.
	defer func() {
		if !s.restartInProgress() {
			conn.Close(websocket.StatusNormalClosure, "")
		}
	}()

	// Register with the restart registry so a daemon shutdown can reach this
	// live conn and close it with a reconnect-triggering code.
	connHandle := s.registerSessionConn(conn)
	defer s.unregisterSessionConn(connHandle)

	// Per-session cancel so `dejima sessions revoke` can forcibly drop us.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	tracker := s.trackerFor(name, spec.ID)
	handle, others := tracker.Attach(label, cancel)
	attachedAt := time.Now().UTC()
	s.recordClientHistory(ClientHistoryEntry{Label: label, Island: name, AttachedAt: attachedAt})
	s.emit(events.Event{
		Type:    events.TypeClientAttached,
		Island:  name,
		Agent:   spec.ID,
		Payload: map[string]any{"label": label},
	})
	defer func() {
		tracker.Detach(handle)
		s.recordClientHistory(ClientHistoryEntry{
			Label: label, Island: name, AttachedAt: attachedAt, DetachedAt: time.Now().UTC(),
		})
		s.emit(events.Event{
			Type:    events.TypeClientDetached,
			Island:  name,
			Agent:   spec.ID,
			Payload: map[string]any{"label": label},
		})
		// "last client" is island-wide: fire only when no agent has any client.
		if len(s.islandPresence(name)) == 0 {
			s.emit(events.Event{Type: events.TypeLastClientDetached, Island: name})
		}
	}()

	// Read envelopes from the WS through this channel so we can race the
	// first one against a short timer (to size the PTY before opening it)
	// without using a child timeout context on conn.Read — coder/websocket
	// closes the whole connection when a Read's ctx fires. The reader uses
	// the long-lived session ctx and exits cleanly on cancel.
	type wsRead struct {
		env *SessionEnvelope
		err error
	}
	envCh := make(chan wsRead)
	go func() {
		defer close(envCh)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				select {
				case envCh <- wsRead{err: err}:
				case <-ctx.Done():
				}
				return
			}
			var env SessionEnvelope
			if jerr := json.Unmarshal(data, &env); jerr != nil {
				s.log.Debug("ws envelope parse", "err", jerr)
				continue
			}
			select {
			case envCh <- wsRead{env: &env}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait briefly for the client's first envelope. The dejima client (see
	// cmd/dejima/main.go's "Send an initial resize" block) sends a resize as
	// its very first message after the WS opens; using it to size the PTY
	// up-front prevents the inner tmux client (and the agent it runs) from
	// rendering at 80x24 and then racing a late SIGWINCH. Non-TTY clients
	// (automation) may not send one — fall through to the PTY default.
	//
	// pending holds a non-resize first envelope so we don't drop client input
	// during the size handshake; in practice it stays nil.
	var (
		initRows, initCols uint16
		initTerm           bridge.TermEnv
		pending            *SessionEnvelope
	)
	select {
	case r := <-envCh:
		if r.err == nil && r.env != nil {
			if r.env.Type == "resize" {
				initRows, initCols = r.env.Rows, r.env.Cols
				initTerm = bridge.TermEnv{Term: r.env.Term, ColorTerm: r.env.ColorTerm}
			} else {
				pending = r.env
			}
		}
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return
	}

	// A sizeless attach (no resize within the handshake window — automation, a
	// status poller, or a client whose resize arrived late) must NOT come up at
	// creack/pty's 0x0 default: under `window-size latest` a 0x0 client becomes
	// the latest client and collapses the shared window to tmux's 80x24 fallback,
	// stomping the real interactive client's size. Match the largest client
	// already attached instead, so the new client can't shrink the window.
	initRows, initCols = s.resolveAttachSize(ctx, p, tmuxName, initRows, initCols)
	sess, err := bridge.AttachToTmux(ctx, "docker", p.ContainerName(), tmuxName, initRows, initCols, initTerm)
	if err != nil {
		_ = sendEnvelope(ctx, conn, SessionEnvelope{Type: "error", B64: err.Error()})
		return
	}
	defer sess.Close()

	// Send initial hello with the existing client list.
	_ = sendEnvelope(ctx, conn, SessionEnvelope{Type: "hello", Attached: others})

	// Replay a non-resize first envelope (rare; see above).
	if pending != nil {
		applyEnvelope(sess, pending)
	}

	// PTY → websocket pump.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				env := SessionEnvelope{Type: "data", B64: encodeB64(buf[:n])}
				if writeErr := sendEnvelope(ctx, conn, env); writeErr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.log.Debug("pty read error", "err", err)
				}
				cancel()
				return
			}
		}
	}()

	// Websocket → PTY pump (and resize handling) — consumes the channel the
	// reader goroutine above is already feeding.
	for {
		select {
		case <-ctx.Done():
			return
		case r, ok := <-envCh:
			if !ok || r.err != nil {
				return
			}
			if !applyEnvelope(sess, r.env) {
				return
			}
		}
	}
}

// applyEnvelope dispatches one client→server envelope to the PTY. Returns
// false if a write fails and the session should tear down.
func applyEnvelope(sess *bridge.PTYSession, env *SessionEnvelope) bool {
	switch env.Type {
	case "data":
		raw, err := decodeB64(env.B64)
		if err != nil {
			return true
		}
		if _, err := sess.Write(raw); err != nil {
			return false
		}
	case "resize":
		_ = sess.Resize(env.Rows, env.Cols)
	}
	return true
}

func sendEnvelope(ctx context.Context, conn *websocket.Conn, env SessionEnvelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// serveTmuxWS runs the WebSocket↔PTY pump for a presence-free tmux session —
// host terminals and in-island shells, which (unlike agent sessions) need no
// per-agent presence tracking. attach opens the PTY at the negotiated size;
// maxSize supplies a fallback size when the client sends no resize (so a sizeless
// attach can't shrink a shared window — see sessionWS). It owns the WebSocket
// from Accept through close. logName/key label the attach/detach log line.
func (s *Server) serveTmuxWS(
	w http.ResponseWriter, r *http.Request, logName, key string,
	attach func(ctx context.Context, rows, cols uint16, te bridge.TermEnv) (*bridge.PTYSession, error),
	maxSize func(ctx context.Context) (uint16, uint16, bool),
) {
	label := r.URL.Query().Get("label")
	if label == "" {
		label = "anonymous"
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.log.Error(logName+" ws accept", "err", err)
		return
	}
	// See sessionWS: during a daemon restart the conn is already closed with the
	// Service-Restart code, so skip the NormalClosure (which would stop the client).
	defer func() {
		if !s.restartInProgress() {
			conn.Close(websocket.StatusNormalClosure, "")
		}
	}()

	connHandle := s.registerSessionConn(conn)
	defer s.unregisterSessionConn(connHandle)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.log.Info(logName+" attached", "key", key, "label", label)
	defer s.log.Info(logName+" detached", "key", key, "label", label)

	type wsRead struct {
		env *SessionEnvelope
		err error
	}
	envCh := make(chan wsRead)
	go func() {
		defer close(envCh)
		for {
			_, data, rerr := conn.Read(ctx)
			if rerr != nil {
				select {
				case envCh <- wsRead{err: rerr}:
				case <-ctx.Done():
				}
				return
			}
			var env SessionEnvelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			select {
			case envCh <- wsRead{env: &env}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var (
		initRows, initCols uint16
		initTerm           bridge.TermEnv
		pending            *SessionEnvelope
	)
	select {
	case rd := <-envCh:
		if rd.err == nil && rd.env != nil {
			if rd.env.Type == "resize" {
				initRows, initCols = rd.env.Rows, rd.env.Cols
				initTerm = bridge.TermEnv{Term: rd.env.Term, ColorTerm: rd.env.ColorTerm}
			} else {
				pending = rd.env
			}
		}
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	if (initRows == 0 || initCols == 0) && maxSize != nil {
		if rr, cc, ok := maxSize(ctx); ok {
			initRows, initCols = rr, cc
		}
	}

	sess, err := attach(ctx, initRows, initCols, initTerm)
	if err != nil {
		_ = sendEnvelope(ctx, conn, SessionEnvelope{Type: "error", B64: err.Error()})
		return
	}
	defer sess.Close()

	_ = sendEnvelope(ctx, conn, SessionEnvelope{Type: "hello"})
	if pending != nil {
		applyEnvelope(sess, pending)
	}

	// PTY → websocket pump.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := sess.Read(buf)
			if n > 0 {
				if sendEnvelope(ctx, conn, SessionEnvelope{Type: "data", B64: encodeB64(buf[:n])}) != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				// The bridged terminal ended: a Ctrl-b d detach, an intentional
				// `exit`, or a tmux that exited instantly (e.g. a failed
				// "open terminal"). Send an explicit, application-level end signal
				// so the client stops cleanly instead of misreading the websocket
				// close as a transport drop and reconnecting forever (respawning a
				// shell the operator can't escape). Best-effort: on a real transport
				// drop ctx is already cancelled, so this no-ops and the client sees
				// an abnormal close and correctly reconnects — the tmux session
				// persists across that. Only a genuine PTY EOF reaches here with a
				// live ctx, so this cleanly discriminates "terminal gone" (stop)
				// from "link blipped" (reconnect).
				//
				// EXCEPTION: during a daemon restart the daemon kills its docker-exec
				// PTYs, so this read errors with a live ctx and would look like a
				// deliberate exit. Skip the "exit" signal when restarting — the conn
				// has already been (or is about to be) closed with the Service-Restart
				// code, which the client reconnects through.
				if !s.restartInProgress() {
					_ = sendEnvelope(ctx, conn, SessionEnvelope{Type: "exit"})
				}
				cancel()
				return
			}
		}
	}()

	// Websocket → PTY pump.
	for {
		select {
		case <-ctx.Done():
			return
		case rd, ok := <-envCh:
			if !ok || rd.err != nil {
				return
			}
			if !applyEnvelope(sess, rd.env) {
				return
			}
		}
	}
}

// islandShellSession is the tmux session name for an island's shared in-island
// shell — the contained "terminal at this island" the dashboard opens on Enter.
const islandShellSession = "dejima-shell"

// islandShellWS attaches an interactive shell at /workspace inside the island's
// container: a single shared tmux session (create-or-attach), resumable and
// multi-client, NOT modeled as an agent. Operator-only (see roleauth).
func (s *Server) islandShellWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	status, err := s.rt.Status(r.Context(), p.ContainerName())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status != runtime.StatusRunning {
		writeError(w, http.StatusConflict,
			fmt.Errorf("island %q is %s; wake it first with `dejima wake %s`", name, status, name))
		return
	}
	if err := s.ensureIslandShell(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	container := p.ContainerName()
	s.serveTmuxWS(w, r, "island shell", name,
		func(ctx context.Context, rows, cols uint16, te bridge.TermEnv) (*bridge.PTYSession, error) {
			return bridge.AttachToTmux(ctx, "docker", container, islandShellSession, rows, cols, te)
		},
		func(ctx context.Context) (uint16, uint16, bool) {
			return bridge.MaxClientSize(ctx, "docker", container, islandShellSession)
		})
}

// ensureIslandShell starts the in-island shell's tmux session at /workspace if
// it isn't already running. Idempotent: a "duplicate session" (it already
// exists) is success — that's the resume case.
func (s *Server) ensureIslandShell(ctx context.Context, p *project.Project) error {
	_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(),
		[]string{"tmux", "new-session", "-d", "-s", islandShellSession, "-c", "/workspace"})
	if err != nil {
		return fmt.Errorf("create in-island shell: %w", err)
	}
	if code != 0 && !strings.Contains(stderr, "duplicate session") {
		return fmt.Errorf("create in-island shell: %s", strings.TrimSpace(stderr))
	}
	return nil
}

// ensureAttachTarget starts the agent's tmux session if it is not up yet, so the
// attach has something real to connect to. Extracted from sessionWS to be
// testable: the decision happens before the websocket upgrade, which needs a
// hijackable connection a test recorder cannot provide.
//
// Best-effort by design. A bare shell in place of an agent is bad; refusing to
// connect at all is worse, so a failure is logged and the attach proceeds.
func (s *Server) ensureAttachTarget(ctx context.Context, p *project.Project, spec *project.AgentSpec, tmuxName string) {
	if ok, _ := s.tmuxHasSession(ctx, p, tmuxName); ok {
		return
	}
	if err := s.ensureAgentSession(ctx, p, spec, false); err != nil {
		s.log.Warn("could not start agent session before attach",
			"island", p.Name, "agent", spec.ID, "err", err)
	}
}

// defaultAttachRows/Cols are the last-resort dimensions for an attach whose size
// could not be determined. They MUST match `default-size` in image/tmux.conf,
// which is what a detached `tmux new-session -d` already uses — so a sizeless
// attach lands on the size the session was created at instead of shrinking it.
// Change the two together.
const (
	defaultAttachRows uint16 = 50
	defaultAttachCols uint16 = 200
)

// resolveAttachSize decides the dimensions an attach comes up at. Its whole job
// is that THE ANSWER IS NEVER ZERO.
//
// Three sources in order: the size the client sent, the largest client already
// attached, and finally the image's own default. The third one is the fix — the
// first two can both fail on a loaded host, and until now the zeros survived.
func (s *Server) resolveAttachSize(ctx context.Context, p *project.Project, tmuxName string, rows, cols uint16) (uint16, uint16) {
	if rows > 0 && cols > 0 {
		return rows, cols
	}
	// Match the largest client already attached, so a sizeless attach cannot
	// shrink the window and even pulls it toward the real client's size.
	if r, c, ok := maxClientSizeFn(ctx, "docker", p.ContainerName(), tmuxName); ok && r > 0 && c > 0 {
		return r, c
	}
	// MaxClientSize reports ok=false both when no client is attached YET and when
	// the query FAILS — and it is itself a `docker exec`, so on a saturated host
	// it loses the same race the handshake just lost. The zeros used to survive
	// to AttachToTmux, which takes creack/pty's unsized branch, and the 0x0
	// client became the "latest" client under tmux's `window-size latest`:
	// exactly the collapse the code above exists to prevent. The guard fell
	// through to the bug it was written for.
	//
	// The symptom that reached us: an operator's 200x50 terminal showing a live
	// 80x24 region and blank everywhere else, with tmux's own status bar still
	// drawn because tmux was never unhealthy. Reported three times as "the
	// terminal went black", on the one island loaded enough to lose both races.
	s.log.Debug("sizeless attach; using the image default rather than 0x0",
		"island", p.Name, "session", tmuxName, "rows", defaultAttachRows, "cols", defaultAttachCols)
	return defaultAttachRows, defaultAttachCols
}

// maxClientSizeFn is indirected so a test can drive the case that matters most:
// tmux ANSWERED and the answer is unusable. bridge.MaxClientSize shells out to
// `docker` directly rather than through the runtime interface, so a fake runtime
// cannot reach it, and the `r > 0 && c > 0` check above was untestable — a
// mutation removing it changed nothing, which is how a guard becomes decoration.
//
// It is reachable in the field: parseMaxClientSize parses a "0 0" client line
// happily and returns ok=true with zeros.
var maxClientSizeFn = bridge.MaxClientSize
