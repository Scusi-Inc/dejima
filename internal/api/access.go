package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/version"
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
	agentID := r.URL.Query().Get("agent")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	follow := r.URL.Query().Get("follow") == "true"

	var rc io.ReadCloser
	primary := p.PrimaryAgent()
	switch {
	case agentID == "" || (primary != nil && agentID == primary.ID):
		// The island/primary logs are the container's stdout/stderr.
		rc, err = s.rt.Logs(r.Context(), p.ContainerName(), follow)
	default:
		a, ok := p.AgentByID(agentID)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, agentID))
			return
		}
		if handlers.Attachable(a.Type) {
			writeError(w, http.StatusConflict,
				fmt.Errorf("agent %q is interactive — attach with `dejima connect %s/%s`; only headless agents have logs", agentID, name, agentID))
			return
		}
		// A co-located headless agent writes to a per-agent log file.
		tailArgs := []string{"tail", "-n", "+1"}
		if follow {
			tailArgs = []string{"tail", "-F", "-n", "+1"}
		}
		tailArgs = append(tailArgs, headlessLogPath(agentID))
		rc, err = s.rt.ExecStream(r.Context(), p.ContainerName(), tailArgs)
	}
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

// handleOverview returns server-wide aggregates: counts, totals, daemon uptime.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	projects, err := project.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := OverviewResponse{
		TotalIslands:         len(projects),
		DaemonStartedAt:      s.startedAt,
		IslandImage:          DefaultImage,
		HostTerminalsEnabled: s.hostTerminals,
		SSHAddr:              s.sshAddr,
		DaemonVersion:        version.Version,
		APIVersion:           version.APIVersion,
	}
	if s.events != nil {
		out.WebhookCount = len(s.events.List())
	}
	// Single Docker query that probes both reachability and image presence.
	if exists, err := s.rt.ImageExists(r.Context(), DefaultImage); err == nil {
		out.DockerReachable = true
		out.IslandImagePresent = exists
	}
	allStats := s.statsAll(r.Context())
	for _, p := range projects {
		status, _ := s.rt.Status(r.Context(), p.ContainerName())
		switch status {
		case runtime.StatusRunning:
			out.Running++
			if stats, ok := allStats[p.ContainerName()]; ok {
				out.MemoryUsageBytes += stats.MemoryUsageBytes
				out.MemoryLimitBytes += stats.MemoryLimitBytes
				out.CPUPercent += stats.CPUPercent
			}
		case runtime.StatusErrored, runtime.StatusMissing:
			out.Errored++
		default:
			if p.DesiredState == project.StateHibernated {
				out.Hibernated++
			}
		}
		out.AttachedClients += len(s.islandPresence(p.Name))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleIslandEvents returns the per-island recent event log (newest first).
func (s *Server) handleIslandEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := project.Load(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, s.IslandEvents(name))
}

// gitStatusOf inspects the workspace of a running island and returns a
// GitInfo. Uses container exec; only called by the detail endpoint, not the
// list. Returns nil on any failure (best-effort).
func (s *Server) gitStatusOf(r *http.Request, containerName string) *GitInfo {
	if status, _ := s.rt.Status(r.Context(), containerName); status != runtime.StatusRunning {
		return nil
	}
	// Quick check: is /workspace a git repo at all?
	if _, _, code, _ := s.rt.Exec(r.Context(), containerName,
		[]string{"git", "-C", "/workspace", "rev-parse", "--git-dir"}); code != 0 {
		return nil
	}
	info := &GitInfo{}

	if out, _, _, _ := s.rt.Exec(r.Context(), containerName,
		[]string{"git", "-C", "/workspace", "rev-parse", "--abbrev-ref", "HEAD"}); out != "" {
		info.Branch = strings.TrimSpace(out)
	}
	if out, _, code, _ := s.rt.Exec(r.Context(), containerName,
		[]string{"git", "-C", "/workspace", "status", "--porcelain"}); code == 0 {
		out = strings.TrimSpace(out)
		if out == "" {
			info.Clean = true
		} else {
			info.DirtyFiles = strings.Count(out, "\n") + 1
		}
	}
	if out, _, code, _ := s.rt.Exec(r.Context(), containerName,
		[]string{"git", "-C", "/workspace", "rev-list", "--count", "@{u}..HEAD"}); code == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
			info.Ahead = n
		}
	}
	if out, _, code, _ := s.rt.Exec(r.Context(), containerName,
		[]string{"git", "-C", "/workspace", "rev-list", "--count", "HEAD..@{u}"}); code == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
			info.Behind = n
		}
	}
	return info
}

var (
	errCmdEmpty = staticErr("cmd is required and must be non-empty")
)

func errIslandNotRunning(name string) error {
	return staticErr("island " + name + " is not running; `dejima wake " + name + "` first")
}
