package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// agentUpdateTimeout is generous on purpose: `npm install -g` into a cold cache
// on a small VM is minutes, not seconds, and a timeout here leaves a
// half-installed agent that the next launch will try to run.
const agentUpdateTimeout = 10 * time.Minute

// handleUpdateAgent upgrades one agent's framework inside a running island, then
// relaunches it so the new binary is what is actually running.
//
// The gap this closes: every self-installing agent's launch line is shaped
// `command -v X || install X`. Install-if-missing, never update — so an island
// pins whatever version it first installed for the life of the container, and
// nothing on any surface said so. The agent's own updater cannot help either:
// OpenClaw's hands off to a service supervisor to restart the process, and we
// launch it directly in tmux, so it reports "managed-service-handoff-unavailable"
// and skips. Neither side is wrong. There was simply no path, and the operator
// was left hand-typing `npm install -g` through `dejima exec`.
//
// The RELAUNCH is not optional and not a convenience. Updating the package while
// the old process keeps running changes the version on disk and not the version
// in memory, so every surface would report the new one while the island ran the
// old — the exact shape of bug this codebase has spent a fortnight removing.
func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	a, ok := p.AgentByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, id))
		return
	}
	h, known := handlers.Lookup(a.Type)
	if !known {
		writeError(w, http.StatusConflict, fmt.Errorf(
			"agent type %q is not in the handler registry, so Dejima does not know how to update it", a.Type))
		return
	}
	if !h.UpdatableInPlace() {
		// Not a failure — a different answer. A bundled agent's version IS the
		// island image's, so naming `dejima upgrade` is the whole point of
		// distinguishing the two.
		writeError(w, http.StatusConflict, fmt.Errorf(
			"%s ships in the island image, so its version comes from the image rather than "+
				"an in-island install — update it with `dejima upgrade %s` (rebuild the image "+
				"first if you are on a source install)", a.Type, name))
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

	var req UpdateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), agentUpdateTimeout)
	defer cancel()
	stdout, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(), h.UpdateCmd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("run update: %w", err))
		return
	}
	if code != 0 {
		// Return the output, not just the code. An install failure is almost
		// always legible in its own last lines (network, registry auth, disk),
		// and a bare exit status sends the operator back in with `dejima exec`.
		writeError(w, http.StatusInternalServerError, fmt.Errorf(
			"update exited %d: %s", code, tail(stderr+stdout, 800)))
		return
	}

	// Relaunch so the running process is the version just installed.
	restarted := true
	if err := s.restartAgentSession(ctx, p, a, req.Resume); err != nil {
		s.log.Warn("agent updated but not relaunched", "island", name, "agent", id, "err", err)
		restarted = false
	}
	s.log.Info("agent updated", "island", name, "agent", id, "cmd", strings.Join(h.UpdateCmd, " "))
	writeJSON(w, http.StatusOK, UpdateAgentResponse{
		Agent:     id,
		Command:   strings.Join(h.UpdateCmd, " "),
		Output:    tail(stdout+stderr, 2000),
		Restarted: restarted,
	})
}

// tail returns the last n bytes, marked when it truncated. Truncation that reads
// as the whole output is how a partial log gets treated as complete.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…(truncated)…\n" + s[len(s)-n:]
}
