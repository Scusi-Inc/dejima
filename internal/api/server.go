package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/islandimage"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

const (
	// DefaultImage is the canonical island image. Built locally from image/Dockerfile.
	DefaultImage = "dejima/island:latest"
	// DefaultAgent is the agent run inside the island when none is specified.
	DefaultAgent = "claude-code"
)

// Server is the Dejima HTTP API server.
type Server struct {
	rt       runtime.Runtime
	log      *slog.Logger
	mu       sync.Mutex
	locks    map[string]*sync.Mutex // per-island
	presence map[string]*presenceTracker
	events   *events.Manager

	// In-memory ring buffer of recent attach/detach events. Bounded so the
	// daemon never accumulates client history indefinitely. Not persisted —
	// daemon restart loses it. Surveillance-free by design.
	historyMu   sync.Mutex
	historyRing []ClientHistoryEntry
	historyCap  int

	// Per-island latest agent state, derived from agent-event hooks.
	agentStateMu sync.Mutex
	agentStates  map[string]AgentStateInfo

	// Per-island bounded event log (for `dejima status` recent-events display
	// and the GET /v1/islands/:name/events endpoint).
	eventsMu  sync.Mutex
	events_   map[string][]events.Event
	eventsCap int

	// Container-stats cache. One `docker stats` sample takes ~2s regardless
	// of container count, and the TUI fires several requests per tick that
	// each want stats — single-flight + short TTL keeps that to one engine
	// query per interval instead of one per island per request.
	statsMu   sync.Mutex
	statsData map[string]runtime.Stats
	statsAt   time.Time

	startedAt time.Time
}

// statsAll returns per-container stats, serving from a short-TTL cache.
// Holding statsMu across the engine query makes concurrent callers wait for
// the in-flight result rather than stacking duplicate `docker stats` calls.
// On query failure it serves the previous snapshot — stale beats absent.
func (s *Server) statsAll(ctx context.Context) map[string]runtime.Stats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if time.Since(s.statsAt) < 2*time.Second {
		return s.statsData
	}
	data, err := s.rt.StatsAll(ctx)
	if err != nil {
		return s.statsData
	}
	s.statsData, s.statsAt = data, time.Now()
	return data
}

// NewServer constructs a server backed by the given runtime.
func NewServer(rt runtime.Runtime, log *slog.Logger, ev *events.Manager) *Server {
	return &Server{
		rt:          rt,
		log:         log,
		locks:       map[string]*sync.Mutex{},
		presence:    map[string]*presenceTracker{},
		events:      ev,
		historyCap:  200,
		agentStates: map[string]AgentStateInfo{},
		events_:     map[string][]events.Event{},
		eventsCap:   50,
		startedAt:   time.Now().UTC(),
	}
}

// recordClientHistory appends an attach/detach event to the ring buffer.
func (s *Server) recordClientHistory(e ClientHistoryEntry) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.historyRing = append(s.historyRing, e)
	if len(s.historyRing) > s.historyCap {
		s.historyRing = s.historyRing[len(s.historyRing)-s.historyCap:]
	}
}

// ClientHistory returns the most recent attach/detach events (newest first).
func (s *Server) ClientHistory() []ClientHistoryEntry {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	out := make([]ClientHistoryEntry, len(s.historyRing))
	for i, e := range s.historyRing {
		out[len(s.historyRing)-1-i] = e
	}
	return out
}

// RevokeAllSessions drops every active websocket client across every island.
// Returns the count of clients that were signaled.
func (s *Server) RevokeAllSessions() int {
	s.mu.Lock()
	trackers := make([]*presenceTracker, 0, len(s.presence))
	for _, t := range s.presence {
		trackers = append(trackers, t)
	}
	s.mu.Unlock()
	total := 0
	for _, t := range trackers {
		total += t.RevokeAll()
	}
	return total
}

// emit fans an event out to webhook subscribers, records it in the per-island
// event log, and updates the island's latest agent-state if applicable.
// Safe to call when events is nil.
func (s *Server) emit(e events.Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	s.recordEvent(e)
	s.maybeUpdateAgentState(e)
	if s.events != nil {
		s.events.Emit(e)
	}
}

// recordEvent appends an event to the per-island bounded ring.
func (s *Server) recordEvent(e events.Event) {
	if e.Island == "" {
		return
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	ring := s.events_[e.Island]
	ring = append(ring, e)
	if len(ring) > s.eventsCap {
		ring = ring[len(ring)-s.eventsCap:]
	}
	s.events_[e.Island] = ring
}

// IslandEvents returns the most recent events for one island (newest first).
func (s *Server) IslandEvents(island string) []events.Event {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	ring := s.events_[island]
	out := make([]events.Event, len(ring))
	for i, e := range ring {
		out[len(ring)-1-i] = e
	}
	return out
}

// maybeUpdateAgentState refreshes the per-island latest agent-state entry
// when the event is one the agent emitted (via its shim).
func (s *Server) maybeUpdateAgentState(e events.Event) {
	if e.Island == "" {
		return
	}
	switch e.Type {
	case events.TypeAgentWaitingForInput,
		events.TypeAgentTaskComplete,
		events.TypeAgentError:
		// fall through
	default:
		return
	}
	short := strings.TrimPrefix(string(e.Type), "agent.")
	s.agentStateMu.Lock()
	s.agentStates[e.Island] = AgentStateInfo{Latest: short, UpdatedAt: e.Timestamp}
	s.agentStateMu.Unlock()
}

// agentStateOf returns the latest agent-state entry for an island, or nil.
func (s *Server) agentStateOf(island string) *AgentStateInfo {
	s.agentStateMu.Lock()
	defer s.agentStateMu.Unlock()
	if st, ok := s.agentStates[island]; ok {
		return &st
	}
	return nil
}

// Handler returns an http.Handler suitable for the daemon's listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/islands", s.listIslands)
	mux.HandleFunc("POST /v1/islands", s.createIsland)
	mux.HandleFunc("GET /v1/islands/{name}", s.getIsland)
	mux.HandleFunc("DELETE /v1/islands/{name}", s.deleteIsland)
	mux.HandleFunc("POST /v1/islands/{name}/hibernate", s.hibernateIsland)
	mux.HandleFunc("POST /v1/islands/{name}/wake", s.wakeIsland)
	mux.HandleFunc("POST /v1/islands/{name}/reset", s.resetIsland)
	mux.HandleFunc("POST /v1/islands/{name}/upgrade", s.upgradeIsland)
	mux.HandleFunc("POST /v1/image/build", s.handleImageBuild)
	mux.HandleFunc("GET /v1/islands/{name}/session", s.sessionWS)
	mux.HandleFunc("GET /v1/healthz", s.healthz)
	mux.HandleFunc("PUT /v1/credentials/claude", s.handlePushClaudeCreds)
	mux.HandleFunc("GET /v1/credentials/claude", s.handleClaudeCredsStatus)
	mux.HandleFunc("GET /v1/events/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /v1/events/subscribe", s.subscribeWebhook)
	mux.HandleFunc("DELETE /v1/events/subscriptions/{id}", s.unsubscribeWebhook)
	mux.HandleFunc("POST /v1/internal/agent-event", s.handleAgentEvent)
	mux.HandleFunc("POST /v1/sessions/revoke", s.handleRevokeSessions)
	mux.HandleFunc("GET /v1/clients", s.handleClientHistory)
	mux.HandleFunc("GET /v1/overview", s.handleOverview)
	mux.HandleFunc("GET /v1/islands/{name}/events", s.handleIslandEvents)
	mux.HandleFunc("POST /v1/islands/{name}/exec", s.handleExec)
	mux.HandleFunc("GET /v1/islands/{name}/files/{path...}", s.handleReadFile)
	mux.HandleFunc("PUT /v1/islands/{name}/files/{path...}", s.handleWriteFile)
	mux.HandleFunc("GET /v1/islands/{name}/logs", s.handleLogs)
	return logMiddleware(s.log, mux)
}

// AdoptExisting brings the runtime state into alignment with persisted project
// state. Called at daemon startup. Best-effort: errors are logged but do not
// prevent the daemon from serving.
func (s *Server) AdoptExisting(ctx context.Context) {
	projects, err := project.List()
	if err != nil {
		s.log.Error("adopt: list projects", "err", err)
		return
	}
	for _, p := range projects {
		status, err := s.rt.Status(ctx, p.ContainerName())
		if err != nil {
			s.log.Warn("adopt: status check failed", "project", p.Name, "err", err)
			continue
		}
		switch {
		case status == runtime.StatusMissing:
			s.log.Warn("adopt: container missing", "project", p.Name, "desired", p.DesiredState)
		case p.DesiredState == project.StateRunning && status != runtime.StatusRunning:
			s.log.Info("adopt: starting container", "project", p.Name)
			if err := s.rt.StartContainer(ctx, p.ContainerName()); err != nil {
				s.log.Error("adopt: start failed", "project", p.Name, "err", err)
			}
		case p.DesiredState == project.StateHibernated && status == runtime.StatusRunning:
			s.log.Info("adopt: stopping container to match hibernated desired state", "project", p.Name)
			if err := s.rt.StopContainer(ctx, p.ContainerName()); err != nil {
				s.log.Error("adopt: stop failed", "project", p.Name, "err", err)
			}
		}
	}
}

// projectLock returns a per-island mutex so concurrent ops serialize.
func (s *Server) projectLock(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.locks[name]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.locks[name] = m
	return m
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePushClaudeCreds stores client-supplied Claude credentials as the seed
// new islands are provisioned from. This is how a logged-in laptop authorizes
// a daemon host that has no Claude login of its own (e.g. headless box where
// the browser OAuth flow is impractical).
func (s *Server) handlePushClaudeCreds(w http.ResponseWriter, r *http.Request) {
	var req PushCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	blob := []byte(req.CredentialsJSON)
	if err := agentcreds.ValidateClaude(blob); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dir, err := paths.ClaudeSeedDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := agentcreds.WriteSeed(dir, blob); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("claude credentials pushed by client")
	w.WriteHeader(http.StatusNoContent)
}

// handleClaudeCredsStatus reports whether new islands will get Claude
// credentials, and from where, without ever returning the secret itself.
func (s *Server) handleClaudeCredsStatus(w http.ResponseWriter, _ *http.Request) {
	var st ClaudeCredentialsStatus
	if _, source, err := agentcreds.LoadClaude(); err == nil {
		st.HostSource = string(source)
	}
	if dir, err := paths.ClaudeSeedDir(); err == nil {
		if info, statErr := os.Stat(filepath.Join(dir, ".credentials.json")); statErr == nil {
			st.SeedPresent = true
			st.SeedUpdatedAt = info.ModTime().UTC()
		}
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) listIslands(w http.ResponseWriter, r *http.Request) {
	projects, err := project.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]IslandInfo, 0, len(projects))
	for _, p := range projects {
		out = append(out, s.toInfo(r.Context(), p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	info := s.toInfo(r.Context(), p)
	// Git status is only computed in the detail view, not the list, because
	// it requires container exec and is the slowest field to populate.
	info.Git = s.gitStatusOf(r, p.ContainerName())
	// Crash health is one extra inspect; detail-only to keep list refreshes cheap.
	if h, err := s.rt.Inspect(r.Context(), p.ContainerName()); err == nil {
		info.Health = &IslandHealth{
			OOMKilled:    h.OOMKilled,
			RestartCount: h.RestartCount,
			ExitCode:     h.ExitCode,
		}
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) createIsland(w http.ResponseWriter, r *http.Request) {
	var req CreateIslandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.Repo) == "" {
		writeError(w, http.StatusBadRequest, errors.New("repo is required"))
		return
	}
	name := req.Name
	if name == "" {
		name = project.DeriveNameFromRepo(req.Repo)
	}
	if err := project.ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	if project.Exists(name) {
		writeError(w, http.StatusConflict, fmt.Errorf("island %q already exists; use --name to disambiguate", name))
		return
	}

	agent := req.Agent
	if agent == "" {
		agent = DefaultAgent
	}
	image := req.Image
	if image == "" {
		image = DefaultImage
	}

	p, err := s.provision(r.Context(), name, req.Repo, agent, image, req.Resources, req.SeedPath)
	if err != nil {
		// Best-effort cleanup: remove anything we created if provisioning failed mid-flight.
		s.log.Error("provision failed; cleaning up", "name", name, "err", err)
		_ = s.teardown(context.Background(), p, true)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.emit(events.Event{Type: events.TypeIslandCreated, Island: p.Name})
	s.emit(events.Event{Type: events.TypeIslandRunning, Island: p.Name})
	writeJSON(w, http.StatusCreated, s.toInfo(r.Context(), p))
}

func (s *Server) deleteIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.teardown(r.Context(), p, true); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandPurged, Island: p.Name})
	w.WriteHeader(http.StatusNoContent)
}

// provision creates the on-disk project, volumes, and a running container.
func (s *Server) provision(ctx context.Context, name, repo, agent, image string, res Resources, seedPath string) (*project.Project, error) {
	exists, err := s.rt.ImageExists(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("check image %s: %w", image, err)
	}
	if !exists {
		return nil, fmt.Errorf("image %s not found locally; build it with `dejima image build`", image)
	}

	now := time.Now().UTC()
	p := &project.Project{
		Name:    name,
		RepoURL: repo,
		Agent:   agent,
		Image:   image,
		Resources: project.Resources{
			Memory: res.Memory,
			CPUs:   res.CPUs,
			Disk:   res.Disk,
		},
		DesiredState: project.StateRunning,
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	if err := project.EnsureProjectSubdirs(name); err != nil {
		return p, err
	}
	if err := p.Save(); err != nil {
		return p, err
	}

	if err := s.rt.EnsureVolume(ctx, p.WorkspaceVolume()); err != nil {
		return p, fmt.Errorf("create workspace volume: %w", err)
	}
	if err := s.rt.EnsureVolume(ctx, p.AgentVolume()); err != nil {
		return p, fmt.Errorf("create agent volume: %w", err)
	}
	if err := s.rt.EnsureNetwork(ctx, p.NetworkName()); err != nil {
		return p, fmt.Errorf("create network: %w", err)
	}

	if err := s.createContainerForProject(ctx, p, seedPath); err != nil {
		return p, err
	}
	return p, nil
}

// createContainerForProject creates the long-lived container for an existing
// project. Used by provision() and reset().
func (s *Server) createContainerForProject(ctx context.Context, p *project.Project, seedPath string) error {
	binds, err := credentialBindMounts()
	if err != nil {
		return err
	}

	// A local-copy seed: mount the host repo read-only so the island can clone
	// from it into its own workspace volume (the silo stays an independent copy).
	// Only meaningful at first provision; the workspace persists across recreate.
	env := map[string]string{
		"DEJIMA_PROJECT_NAME": p.Name,
		"DEJIMA_REPO_URL":     p.RepoURL,
		"DEJIMA_AGENT":        p.Agent,
		"DEJIMA_SOCKET":       "/run/dejima/dejimad.sock",
	}
	if seedPath != "" {
		binds = append(binds, runtime.BindMount{
			HostPath:      seedPath,
			ContainerPath: "/opt/host/seed",
			ReadOnly:      true,
		})
		env["DEJIMA_SEED"] = "/opt/host/seed"
	}

	// Mount the daemon's Unix socket into the container so per-agent shims can
	// emit events via the internal API endpoint. Only possible when Docker is
	// native to the daemon's host: when dejimad runs on macOS the engine lives
	// in a VM (colima/Docker Desktop) that shares the host fs over virtiofs/sshfs,
	// and unix sockets can't be bind-mounted through that share — Docker tries to
	// mkdir the source path and fails with "operation not supported", aborting the
	// whole run. Skip the mount there; notify.sh already no-ops without the socket.
	if goruntime.GOOS == "linux" {
		if socket, err := paths.SocketPath(); err == nil {
			if _, statErr := os.Stat(socket); statErr == nil {
				binds = append(binds, runtime.BindMount{
					HostPath:      socket,
					ContainerPath: "/run/dejima/dejimad.sock",
					ReadOnly:      false,
				})
			}
		}
	}

	req := runtime.CreateRequest{
		Name:  p.ContainerName(),
		Image: p.Image,
		Env:   env,
		Volumes: []runtime.VolumeMount{
			{Name: p.WorkspaceVolume(), Target: "/workspace"},
			{Name: p.AgentVolume(), Target: agentStateMountTarget(p.Agent)},
		},
		BindMounts:  binds,
		Memory:      p.Resources.Memory,
		CPUs:        p.Resources.CPUs,
		StorageSize: p.Resources.Disk,
		Network:     p.NetworkName(),
		Labels: map[string]string{
			"dejima.project": p.Name,
			"dejima.agent":   p.Agent,
		},
	}
	if _, err := s.rt.CreateContainer(ctx, req); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// teardown removes the container, volumes, network, and on-host config dir.
func (s *Server) teardown(ctx context.Context, p *project.Project, force bool) error {
	if p == nil {
		return nil
	}
	_ = s.rt.RemoveContainer(ctx, p.ContainerName(), force)
	_ = s.rt.RemoveVolume(ctx, p.WorkspaceVolume(), force)
	_ = s.rt.RemoveVolume(ctx, p.AgentVolume(), force)
	_ = s.rt.RemoveNetwork(ctx, p.NetworkName())
	return project.Delete(p.Name)
}

// hibernateIsland gracefully stops a running island's container, preserving volumes.
func (s *Server) hibernateIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.rt.StopContainer(r.Context(), p.ContainerName()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("stop container: %w", err))
		return
	}
	p.DesiredState = project.StateHibernated
	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandHibernated, Island: p.Name})
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// wakeIsland starts a hibernated island's container against existing volumes.
func (s *Server) wakeIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

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

	switch status {
	case runtime.StatusMissing:
		// Container was removed; recreate it against the existing volumes.
		if err := s.createContainerForProject(r.Context(), p, ""); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	case runtime.StatusRunning:
		// No-op; already awake.
	default:
		if err := s.rt.StartContainer(r.Context(), p.ContainerName()); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("start container: %w", err))
			return
		}
	}

	p.DesiredState = project.StateRunning
	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandWoken, Island: p.Name})
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// resetIsland clears the agent on-disk state volume, preserving the workspace.
func (s *Server) resetIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	wasRunning := false
	if status, _ := s.rt.Status(r.Context(), p.ContainerName()); status == runtime.StatusRunning {
		wasRunning = true
	}

	// Stop + remove the container so we can rebuild it against a fresh agent volume.
	_ = s.rt.StopContainer(r.Context(), p.ContainerName())
	_ = s.rt.RemoveContainer(r.Context(), p.ContainerName(), true)

	if err := s.rt.RemoveVolume(r.Context(), p.AgentVolume(), true); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("remove agent volume: %w", err))
		return
	}
	if err := s.rt.EnsureVolume(r.Context(), p.AgentVolume()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("recreate agent volume: %w", err))
		return
	}
	if err := s.rt.EnsureNetwork(r.Context(), p.NetworkName()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("ensure network: %w", err))
		return
	}
	// reset preserves the workspace volume, so no re-clone happens; no seed.
	if err := s.createContainerForProject(r.Context(), p, ""); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Honor the prior desired state. If the island was hibernated when reset
	// was requested, leave the new container stopped.
	if !wasRunning && p.DesiredState == project.StateHibernated {
		_ = s.rt.StopContainer(r.Context(), p.ContainerName())
	}

	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandReset, Island: p.Name})
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// upgradeIsland recreates the container against the current island image while
// preserving BOTH volumes (workspace and agent state). Besides picking up a
// freshly built image, recreating also re-assembles bind mounts, so islands
// created before a daemon upgrade gain any newly introduced mounts (e.g. the
// claude-seed credentials mount) without losing state.
func (s *Server) upgradeIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	wasRunning := false
	if status, _ := s.rt.Status(r.Context(), p.ContainerName()); status == runtime.StatusRunning {
		wasRunning = true
	}

	_ = s.rt.StopContainer(r.Context(), p.ContainerName())
	_ = s.rt.RemoveContainer(r.Context(), p.ContainerName(), true)

	if err := s.rt.EnsureNetwork(r.Context(), p.NetworkName()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("ensure network: %w", err))
		return
	}
	// Both volumes persist; the new container mounts them as-is. No seed —
	// the workspace already holds the clone.
	if err := s.createContainerForProject(r.Context(), p, ""); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Honor the prior desired state, as reset does.
	if !wasRunning && p.DesiredState == project.StateHibernated {
		_ = s.rt.StopContainer(r.Context(), p.ContainerName())
	}

	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandUpgraded, Island: p.Name})
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// handleImageBuild rebuilds the island image from the build context embedded
// in the dejimad binary, streaming combined docker-build output as text/plain.
// A build failure is reported in-stream as a trailing "ERROR: …" line (the
// status code is already sent by then); the client converts it back to an error.
func (s *Server) handleImageBuild(w http.ResponseWriter, r *http.Request) {
	dir, cleanup, err := islandimage.Materialize()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("materialize build context: %w", err))
		return
	}
	defer cleanup()

	stream, err := s.rt.BuildImage(r.Context(), dir, islandimage.Dockerfile, DefaultImage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client went away; ctx cancellation kills the build
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			s.log.Info("island image rebuilt", "image", DefaultImage)
			fmt.Fprintf(w, "\n%s\n", imageBuildOKMarker)
			return
		}
		if readErr != nil {
			s.log.Error("island image build failed", "error", readErr)
			fmt.Fprintf(w, "\nERROR: %v\n", readErr)
			return
		}
	}
}

// imageBuildOKMarker terminates a successful build stream so clients can tell
// success from a build that died mid-stream.
const imageBuildOKMarker = "--- dejima: image build succeeded ---"

func (s *Server) toInfo(ctx context.Context, p *project.Project) IslandInfo {
	info := IslandInfo{
		Name:       p.Name,
		Repo:       p.RepoURL,
		Agent:      p.Agent,
		Image:      p.Image,
		State:      string(p.DesiredState),
		CreatedAt:  p.CreatedAt,
		LastUsedAt: p.LastUsedAt,
	}
	if status, err := s.rt.Status(ctx, p.ContainerName()); err == nil {
		info.Container = string(status)
		if status == runtime.StatusRunning {
			if stats, ok := s.statsAll(ctx)[p.ContainerName()]; ok {
				info.Stats = &IslandStats{
					MemoryUsageBytes: stats.MemoryUsageBytes,
					MemoryLimitBytes: stats.MemoryLimitBytes,
					CPUPercent:       stats.CPUPercent,
				}
			}
		}
	} else {
		info.Container = string(runtime.StatusErrored)
	}
	if tracker := s.presence[p.Name]; tracker != nil {
		info.Attached = tracker.Snapshot()
	}
	info.AgentState = s.agentStateOf(p.Name)
	return info
}

// agentStateMountTarget returns the container path where the agent volume should
// be mounted, based on which agent the project runs. Each agent has its own
// conventional state dir.
func agentStateMountTarget(agent string) string {
	switch agent {
	case "codex":
		return "/home/dejima/.codex"
	case "claude-code", "":
		return "/home/dejima/.claude"
	default:
		return "/home/dejima/.agent-state"
	}
}

// credentialBindMounts assembles the host paths to mount read-only into the island.
// Missing paths are silently skipped so users without `gh` configured can still init.
func credentialBindMounts() ([]runtime.BindMount, error) {
	var binds []runtime.BindMount

	ghDir, err := paths.HostGHConfigDir()
	if err == nil {
		if _, statErr := os.Stat(ghDir); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath: ghDir, ContainerPath: "/opt/host/gh-config", ReadOnly: true,
			})
		}
	}

	claudeDir, err := paths.HostClaudeDir()
	if err == nil {
		if _, statErr := os.Stat(claudeDir); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath: claudeDir, ContainerPath: "/opt/host/claude", ReadOnly: true,
			})
		}
	}

	// Materialized Claude credentials. On macOS hosts the OAuth blob lives in
	// the login Keychain, never in ~/.claude, so the dir mount above carries no
	// credentials there. Refresh the seed from the freshest local source each
	// time a container is created; when no local source exists (headless host
	// that never logged in), a copy previously stored via `dejima auth push`
	// survives untouched.
	if seedDir, err := paths.ClaudeSeedDir(); err == nil {
		if blob, _, err := agentcreds.LoadClaude(); err == nil {
			_, _ = agentcreds.WriteSeed(seedDir, blob)
		}
		if _, statErr := os.Stat(filepath.Join(seedDir, ".credentials.json")); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath: seedDir, ContainerPath: "/opt/host/claude-seed", ReadOnly: true,
			})
		}
	}

	codexDir, err := paths.HostCodexDir()
	if err == nil {
		if _, statErr := os.Stat(codexDir); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath: codexDir, ContainerPath: "/opt/host/codex", ReadOnly: true,
			})
		}
	}

	gitConfig, err := paths.HostGitConfig()
	if err == nil {
		if _, statErr := os.Stat(gitConfig); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath: gitConfig, ContainerPath: "/opt/host/gitconfig", ReadOnly: true,
			})
		}
	}
	return binds, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, ErrorResponse{Error: err.Error()})
}

func logMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("api request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
