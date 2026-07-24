package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bufio"
	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/secrets"
	"github.com/aoos/dejima/internal/version"
	"github.com/aoos/dejima/internal/vmmem"
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

	// `docker cp` preserves the SOURCE file's mode, and the daemon's temp file is
	// 0600 owned by the daemon's host uid (e.g. 501 on macOS) — which the in-island
	// agent (uid 1000) then can't read. Make it world-readable so a copied-in file
	// (e.g. an image for the agent to ingest) is agent-readable with no in-container
	// chmod (we can't chown: `docker exec` runs as the non-root container user).
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

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

	// Mask the island's own secrets on the way out. A tool echoing its
	// configuration is the likeliest way one of these actually leaks, and logs
	// get pasted into issues and chats far more casually than credentials do.
	//
	// Line-oriented, NOT per-read-chunk: a fixed-size read can split a value
	// across two buffers, and a substring replace would then miss both halves
	// while looking like it worked. Splitting on newlines means a value is only
	// missed if it contains one, which the format already escapes.
	redact := secretRedactor(name)
	br := bufio.NewReader(rc)
	for {
		line, readErr := br.ReadString('\n')
		if line != "" {
			if _, writeErr := io.WriteString(w, redact(line)); writeErr != nil {
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

// secretRedactor returns a function masking island's stored secret values.
//
// The store is read ONCE per stream rather than per line: a follow stream can
// run for hours, and re-reading the keychain for every line would be both slow
// and a stream of keychain access prompts. A secret set mid-stream therefore
// isn't masked until the next `dejima logs` — acceptable, since the value
// wasn't in the process's environment when those lines were written either.
//
// Returns identity when the island has no secrets, so the common case costs
// nothing.
func secretRedactor(island string) func(string) string {
	store, err := secrets.OpenIsland()
	if err != nil {
		return func(s string) string { return s }
	}
	return store.Redactor(island)
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
	// Private visibility (P2): a teammate's overview reflects only its OWN islands;
	// the host owner sees the whole fleet. Substrate/host facts below stay
	// host-wide (they're not per-owner); the privacy-preserving cross-tenant
	// rollup is the separate /v1/aggregate (P3).
	visible := projects[:0]
	for _, p := range projects {
		if s.visibleTo(r.Context(), p) {
			visible = append(visible, p)
		}
	}
	projects = visible
	out := OverviewResponse{
		TotalIslands:         len(projects),
		DaemonStartedAt:      s.startedAt,
		IslandImage:          DefaultImage,
		HostTerminalsEnabled: s.hostTerminals,
		SSHAddr:              s.sshAddr,
		SSHHostKey:           s.sshHostKey,
		DaemonVersion:        version.Version,
		APIVersion:           version.APIVersion,
		Panicked:             panicEngaged(),
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
				// The runtime's memory ceiling = the largest per-container limit
				// (an uncapped island reports the whole VM; a capped one is ≤ it),
				// so the max across running islands ≈ the VM total — with no extra
				// docker spawn here.
				if stats.MemoryLimitBytes > out.VMMemoryBytes {
					out.VMMemoryBytes = stats.MemoryLimitBytes
				}
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
	out.HostMemoryBytes = vmmem.HostMemoryBytes()
	out.VMRecommendedBytes = vmmem.RecommendedBytes(out.HostMemoryBytes)
	// Stamp the caller's own identity (multi-tenant "who am I") so a client can
	// drive its own-vs-all view without a second request.
	if id, ok := IdentityFromContext(r.Context()); ok {
		out.Owner = id.Owner
		out.Role = string(id.Role)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAggregate returns the privacy-preserving host-wide rollup: counts +
// total mem/cpu/disk across ALL islands, regardless of owner, so any
// authenticated caller (teammate included) can see shared-host utilization
// WITHOUT seeing what runs. Deliberately NOT owner-filtered (that's the point,
// vs the owner-scoped overview) and carries NO names, repos, owners, or
// per-island rows. Mem/cpu are summed the same way as OverviewResponse.
func (s *Server) handleAggregate(w http.ResponseWriter, r *http.Request) {
	projects, err := project.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := AggregateResponse{TotalIslands: len(projects)}
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
		default:
			if p.DesiredState == project.StateHibernated {
				out.Hibernated++
			}
		}
	}
	for _, sz := range s.volumeSizes(r.Context()) {
		if sz > 0 {
			out.DiskTotalBytes += sz
		}
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
// GitInfo. Uses container exec; called by the detail endpoint and the purge
// guard. Returns nil on any failure or if the workspace isn't a git repo
// (best-effort).
func (s *Server) gitStatusOf(ctx context.Context, containerName string) *GitInfo {
	if status, _ := s.rt.Status(ctx, containerName); status != runtime.StatusRunning {
		return nil
	}
	// Quick check: is /workspace a git repo at all?
	if _, _, code, _ := s.rt.Exec(ctx, containerName,
		[]string{"git", "-C", "/workspace", "rev-parse", "--git-dir"}); code != 0 {
		return nil
	}
	info := &GitInfo{}

	if out, _, _, _ := s.rt.Exec(ctx, containerName,
		[]string{"git", "-C", "/workspace", "rev-parse", "--abbrev-ref", "HEAD"}); out != "" {
		info.Branch = strings.TrimSpace(out)
	}
	if out, _, code, _ := s.rt.Exec(ctx, containerName,
		[]string{"git", "-C", "/workspace", "status", "--porcelain"}); code == 0 {
		out = strings.TrimSpace(out)
		if out == "" {
			info.Clean = true
		} else {
			info.DirtyFiles = strings.Count(out, "\n") + 1
		}
	}
	if out, _, code, _ := s.rt.Exec(ctx, containerName,
		[]string{"git", "-C", "/workspace", "rev-list", "--count", "@{u}..HEAD"}); code == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
			info.Ahead = n
		}
	}
	if out, _, code, _ := s.rt.Exec(ctx, containerName,
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
