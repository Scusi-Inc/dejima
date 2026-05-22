package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// handleExec runs a one-shot command inside an island.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if status, _ := s.rt.Status(r.Context(), p.ContainerName()); status != runtime.StatusRunning {
		writeError(w, http.StatusConflict, errIslandNotRunning(name))
		return
	}
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Cmd) == 0 {
		writeError(w, http.StatusBadRequest, errCmdEmpty)
		return
	}
	stdout, stderr, code, err := s.rt.Exec(r.Context(), p.ContainerName(), req.Cmd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ExecResponse{Stdout: stdout, Stderr: stderr, ExitCode: code})
}

// handleReadFile streams a single file out of the island.
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	target := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	tmp, err := os.CreateTemp("", "dejima-cp-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	if err := s.rt.CopyFromContainer(r.Context(), p.ContainerName(), target, tmp.Name()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		s.log.Debug("file write to client", "err", err)
	}
}

// handleWriteFile copies request body into a file inside the island.
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	target := "/" + strings.TrimPrefix(r.PathValue("path"), "/")

	tmp, err := os.CreateTemp("", "dejima-cp-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r.Body); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tmp.Close()

	// docker cp wants a path that exists; ensure parent dir on the container side.
	parent := filepath.Dir(target)
	_, _, _, _ = s.rt.Exec(r.Context(), p.ContainerName(), []string{"mkdir", "-p", parent})
	if err := s.rt.CopyToContainer(r.Context(), p.ContainerName(), tmp.Name(), target); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLogs streams an island's container logs. ?follow=true keeps the stream
// open until the client disconnects.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	follow := r.URL.Query().Get("follow") == "true"
	rc, err := s.rt.Logs(r.Context(), p.ContainerName(), follow)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// handleRevokeSessions drops every active client websocket.
func (s *Server) handleRevokeSessions(w http.ResponseWriter, _ *http.Request) {
	count := s.RevokeAllSessions()
	writeJSON(w, http.StatusOK, map[string]int{"revoked": count})
}

// handleClientHistory returns the recent attach/detach events (newest first).
func (s *Server) handleClientHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ClientHistory())
}

var (
	errCmdEmpty = staticErr("cmd is required and must be non-empty")
)

func errIslandNotRunning(name string) error {
	return staticErr("island " + name + " is not running; `dejima wake " + name + "` first")
}