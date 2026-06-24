package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/bridge"
	"github.com/aoos/dejima/internal/hostterm"
	"github.com/aoos/dejima/internal/hosttmux"
)

// Host terminals: operator shells running in tmux on the DAEMON HOST (no
// container). Uncontained and operator-only — every handler here gates on the
// --host-terminals capability, and tokenauth keeps these routes unreachable by
// an island token. See docs/host-terminals.md.

// TerminalsResponse is the body of GET /v1/terminals.
type TerminalsResponse struct {
	Terminals []hostterm.Terminal `json:"terminals"`
}

// CreateTerminalRequest is the body of POST /v1/terminals (label optional).
type CreateTerminalRequest struct {
	Label string `json:"label,omitempty"`
}

// RelabelTerminalRequest is the body of PATCH /v1/terminals/{id}.
type RelabelTerminalRequest struct {
	Label string `json:"label"`
}

// requireHostTerminals writes a 403 (with how to enable) when the feature is off.
func (s *Server) requireHostTerminals(w http.ResponseWriter) bool {
	if !s.hostTerminals {
		writeError(w, http.StatusForbidden,
			errors.New("host terminals are disabled; start dejimad with --host-terminals"))
		return false
	}
	return true
}

func (s *Server) handleListTerminals(w http.ResponseWriter, _ *http.Request) {
	if !s.requireHostTerminals(w) {
		return
	}
	store, err := hostterm.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, TerminalsResponse{Terminals: store.Terminals})
}

func (s *Server) handleCreateTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostTerminals(w) {
		return
	}
	var req CreateTerminalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	var created hostterm.Terminal
	if _, err := hostterm.Update(func(st *hostterm.Store) error {
		created = hostterm.Terminal{ID: st.NextID(), Label: strings.TrimSpace(req.Label), CreatedAt: time.Now().UTC()}
		st.Add(created)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Best-effort: bring the detached tmux session up now so the terminal is live
	// immediately. Attach also creates it (new-session -A), so this is just nicety.
	if err := startHostTmux(r.Context(), created.Tmux()); err != nil {
		s.log.Warn("start host tmux failed (will create on attach)", "id", created.ID, "err", err)
	}
	s.log.Info("host terminal created", "id", created.ID, "label", created.Label)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleDeleteTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostTerminals(w) {
		return
	}
	id := r.PathValue("id")
	var tm hostterm.Terminal
	var existed bool
	if _, err := hostterm.Update(func(st *hostterm.Store) error {
		tm, existed = st.Get(id)
		st.Remove(id)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !existed {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such terminal %q", id))
		return
	}
	_ = killHostTmux(r.Context(), tm.Tmux())
	s.log.Info("host terminal removed", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRelabelTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostTerminals(w) {
		return
	}
	id := r.PathValue("id")
	var req RelabelTerminalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	var tm hostterm.Terminal
	var ok bool
	if _, err := hostterm.Update(func(st *hostterm.Store) error {
		ok = st.Relabel(id, req.Label)
		tm, _ = st.Get(id)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such terminal %q", id))
		return
	}
	writeJSON(w, http.StatusOK, tm)
}

// terminalSessionWS attaches a websocket to a host terminal's tmux session,
// mirroring sessionWS but bridging to the daemon host (no container) via
// bridge.AttachToHostTmux. Operator-only and gated; audited on attach/detach.
func (s *Server) terminalSessionWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostTerminals(w) {
		return
	}
	id := r.PathValue("id")
	store, err := hostterm.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	term, ok := store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such terminal %q", id))
		return
	}
	label := r.URL.Query().Get("label")
	if label == "" {
		label = "anonymous"
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.log.Error("terminal ws accept", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.log.Info("host terminal attached", "id", id, "label", label)
	defer s.log.Info("host terminal detached", "id", id, "label", label)

	// Read client envelopes through a channel so we can race the first one
	// against a short timer to size the PTY up-front (see sessionWS for why).
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
		pending            *SessionEnvelope
	)
	select {
	case rd := <-envCh:
		if rd.err == nil && rd.env != nil {
			if rd.env.Type == "resize" {
				initRows, initCols = rd.env.Rows, rd.env.Cols
			} else {
				pending = rd.env
			}
		}
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	if initRows == 0 || initCols == 0 {
		if rr, cc, ok := bridge.HostMaxClientSize(ctx, term.Tmux()); ok {
			initRows, initCols = rr, cc
		}
	}

	sess, err := bridge.AttachToHostTmux(ctx, term.Tmux(), initRows, initCols)
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

// --- host tmux lifecycle (runs on the daemon host directly, no container) ---

// startHostTmux brings a detached host tmux session up; a pre-existing session
// is not an error (idempotent).
func startHostTmux(ctx context.Context, session string) error {
	argv := hosttmux.NewSessionArgs("new-session", "-d", "-s", session)
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil && strings.Contains(string(out), "duplicate session") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// killHostTmux tears down a host tmux session (best-effort).
func killHostTmux(ctx context.Context, session string) error {
	return exec.CommandContext(ctx, "tmux", "kill-session", "-t", session).Run()
}
