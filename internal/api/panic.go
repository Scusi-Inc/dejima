package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// panicEngaged reports whether the ~/.dejima/PANIC flag is present. While it is,
// AdoptExisting refuses to auto-start any island at daemon startup.
func panicEngaged() bool {
	p, err := paths.PanicFlagPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// panicReason returns the human note recorded in the PANIC flag, if any.
func panicReason() string {
	p, err := paths.PanicFlagPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writePanicFlag(reason string) error {
	p, err := paths.PanicFlagPath()
	if err != nil {
		return err
	}
	body := time.Now().UTC().Format(time.RFC3339)
	if reason = strings.TrimSpace(reason); reason != "" {
		body += " " + reason
	}
	return os.WriteFile(p, []byte(body+"\n"), 0o600)
}

func removePanicFlag() error {
	p, err := paths.PanicFlagPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// handlePanic engages the panic stop: it writes the PANIC flag (so a daemon
// restart won't auto-start anything) and stops every running island's container.
// DesiredState is deliberately left untouched, so clearing panic restores each
// island to what it was meant to be.
func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request) {
	var req PanicRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // body optional

	if err := writePanicFlag(req.Reason); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	projects, err := project.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stopped := 0
	for _, p := range projects {
		lock := s.projectLock(p.Name)
		lock.Lock()
		status, _ := s.rt.Status(r.Context(), p.ContainerName())
		if status == runtime.StatusRunning {
			if err := s.rt.StopContainer(r.Context(), p.ContainerName()); err != nil {
				s.log.Error("panic: stop container", "island", p.Name, "err", err)
			} else {
				stopped++
			}
		}
		lock.Unlock()
	}
	s.RevokeAllSessions()
	s.emit(events.Event{Type: events.TypePanicEngaged, Payload: map[string]any{"stopped": stopped, "reason": req.Reason}})
	s.log.Warn("panic engaged", "stopped", stopped, "reason", req.Reason)
	writeJSON(w, http.StatusOK, PanicResponse{Panicked: true, Affected: stopped, Reason: panicReason()})
}

// handleUnpanic clears the PANIC flag and restarts every island whose desired
// state is running, bringing the host back to its intended state.
func (s *Server) handleUnpanic(w http.ResponseWriter, r *http.Request) {
	if err := removePanicFlag(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	projects, err := project.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	restarted := 0
	for _, p := range projects {
		if p.DesiredState != project.StateRunning {
			continue
		}
		lock := s.projectLock(p.Name)
		lock.Lock()
		if s.restartToRunning(r.Context(), p) {
			restarted++
		}
		lock.Unlock()
	}
	s.emit(events.Event{Type: events.TypePanicCleared, Payload: map[string]any{"restarted": restarted}})
	s.log.Info("panic cleared", "restarted", restarted)
	writeJSON(w, http.StatusOK, PanicResponse{Panicked: false, Affected: restarted})
}

// handlePanicStatus reports whether panic mode is currently engaged.
func (s *Server) handlePanicStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, PanicResponse{Panicked: panicEngaged(), Reason: panicReason()})
}

// restartToRunning brings a single island up to a running container, recreating
// it if the container is missing. Returns true if it ended up running. Caller
// holds the island lock. Mirrors the start path in AdoptExisting.
func (s *Server) restartToRunning(ctx context.Context, p *project.Project) bool {
	status, err := s.rt.Status(ctx, p.ContainerName())
	if err != nil {
		s.log.Error("unpanic: status", "island", p.Name, "err", err)
		return false
	}
	switch status {
	case runtime.StatusRunning:
		// already up
	case runtime.StatusMissing:
		if err := s.createContainerForProject(ctx, p, ""); err != nil {
			s.log.Error("unpanic: recreate container", "island", p.Name, "err", err)
			return false
		}
	default:
		if err := s.rt.StartContainer(ctx, p.ContainerName()); err != nil {
			s.log.Error("unpanic: start container", "island", p.Name, "err", err)
			return false
		}
	}
	s.reconcileAgentsAsync(p)
	return true
}
