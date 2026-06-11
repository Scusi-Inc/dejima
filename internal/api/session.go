package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/bridge"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

const tmuxSession = "dejima"

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

type presenceHandle struct{}

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
type SessionEnvelope struct {
	Type     string          `json:"type"`
	B64      string          `json:"b64,omitempty"`
	Rows     uint16          `json:"rows,omitempty"`
	Cols     uint16          `json:"cols,omitempty"`
	Attached []PresenceEntry `json:"attached,omitempty"`
}

// trackerFor returns (or lazily creates) the presence tracker for a project name.
func (s *Server) trackerFor(name string) *presenceTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.presence == nil {
		s.presence = map[string]*presenceTracker{}
	}
	t, ok := s.presence[name]
	if !ok {
		t = newPresenceTracker()
		s.presence[name] = t
	}
	return t
}

func (s *Server) sessionWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	label := r.URL.Query().Get("label")
	if label == "" {
		label = "anonymous"
	}

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if p.Agent == AgentHeadless {
		// Headless islands run a user-supplied command as their main process
		// — there's no tmux to attach to. Surface that as a precondition
		// failure with the right next step.
		writeError(w, http.StatusConflict,
			fmt.Errorf("island %q is headless; it has no attach surface — use `dejima logs %s --follow`", name, name))
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

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // local trust boundary; remote auth handled at TCP listener (M4)
	})
	if err != nil {
		s.log.Error("ws accept", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Per-session cancel so `dejima sessions revoke` can forcibly drop us.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	tracker := s.trackerFor(name)
	handle, others := tracker.Attach(label, cancel)
	attachedAt := time.Now().UTC()
	s.recordClientHistory(ClientHistoryEntry{Label: label, Island: name, AttachedAt: attachedAt})
	s.emit(events.Event{
		Type:    events.TypeClientAttached,
		Island:  name,
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
			Payload: map[string]any{"label": label},
		})
		if len(tracker.Snapshot()) == 0 {
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
		pending            *SessionEnvelope
	)
	select {
	case r := <-envCh:
		if r.err == nil && r.env != nil {
			if r.env.Type == "resize" {
				initRows, initCols = r.env.Rows, r.env.Cols
			} else {
				pending = r.env
			}
		}
		s.log.Info("RESIZEDBG handshake first envelope", "island", name, "label", label,
			"err", r.err, "nil_env", r.env == nil,
			"type", func() string { if r.env != nil { return r.env.Type }; return "" }(),
			"rows", initRows, "cols", initCols)
	case <-time.After(500 * time.Millisecond):
		s.log.Info("RESIZEDBG handshake TIMEOUT (no first envelope in 500ms)", "island", name, "label", label)
	case <-ctx.Done():
		return
	}

	// A sizeless attach (no resize within the handshake window — automation, a
	// status poller, or a client whose resize arrived late) must NOT come up at
	// creack/pty's 0x0 default: under `window-size latest` a 0x0 client becomes
	// the latest client and collapses the shared window to tmux's 80x24 fallback,
	// stomping the real interactive client's size. Match the largest client
	// already attached instead, so the new client can't shrink the window.
	if initRows == 0 || initCols == 0 {
		if r, c, ok := bridge.MaxClientSize(ctx, "docker", p.ContainerName(), tmuxSession); ok {
			initRows, initCols = r, c
			s.log.Info("RESIZEDBG sizeless attach -> matched largest client", "island", name, "label", label,
				"rows", initRows, "cols", initCols)
		}
	}

	s.log.Info("RESIZEDBG attaching", "island", name, "label", label, "init_rows", initRows, "init_cols", initCols)
	sess, err := bridge.AttachToTmux(ctx, "docker", p.ContainerName(), tmuxSession, initRows, initCols)
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
			if r.env != nil && r.env.Type == "resize" {
				s.log.Info("RESIZEDBG apply live resize", "island", name, "label", label, "rows", r.env.Rows, "cols", r.env.Cols)
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
