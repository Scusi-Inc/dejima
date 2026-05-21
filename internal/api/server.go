package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/events"
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
}

// NewServer constructs a server backed by the given runtime.
func NewServer(rt runtime.Runtime, log *slog.Logger, ev *events.Manager) *Server {
	return &Server{
		rt:       rt,
		log:      log,
		locks:    map[string]*sync.Mutex{},
		presence: map[string]*presenceTracker{},
		events:   ev,
	}
}

// emit fans an event out to webhook subscribers. Safe to call when events is nil.
func (s *Server) emit(e events.Event) {
	if s.events == nil {
		return
	}
	s.events.Emit(e)
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
	mux.HandleFunc("GET /v1/islands/{name}/session", s.sessionWS)
	mux.HandleFunc("GET /v1/healthz", s.healthz)
	mux.HandleFunc("GET /v1/events/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /v1/events/subscribe", s.subscribeWebhook)
	mux.HandleFunc("DELETE /v1/events/subscriptions/{id}", s.unsubscribeWebhook)
	mux.HandleFunc("POST /v1/internal/agent-event", s.handleAgentEvent)
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
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
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

	p, err := s.provision(r.Context(), name, req.Repo, agent, image, req.Resources)
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
func (s *Server) provision(ctx context.Context, name, repo, agent, image string, res Resources) (*project.Project, error) {
	exists, err := s.rt.ImageExists(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("check image %s: %w", image, err)
	}
	if !exists {
		return nil, fmt.Errorf("image %s not found locally; build it with `make image` first", image)
	}

	now := time.Now().UTC()
	p := &project.Project{
		Name:         name,
		RepoURL:      repo,
		Agent:        agent,
		Image:        image,
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

	if err := s.createContainerForProject(ctx, p); err != nil {
		return p, err
	}
	return p, nil
}

// createContainerForProject creates the long-lived container for an existing
// project. Used by provision() and reset().
func (s *Server) createContainerForProject(ctx context.Context, p *project.Project) error {
	binds, err := credentialBindMounts()
	if err != nil {
		return err
	}

	// Mount the daemon's Unix socket into the container so per-agent shims can
	// emit events via the internal API endpoint.
	if socket, err := paths.SocketPath(); err == nil {
		if _, statErr := os.Stat(socket); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath:      socket,
				ContainerPath: "/run/dejima/dejimad.sock",
				ReadOnly:      false,
			})
		}
	}

	req := runtime.CreateRequest{
		Name:  p.ContainerName(),
		Image: p.Image,
		Env: map[string]string{
			"DEJIMA_PROJECT_NAME": p.Name,
			"DEJIMA_REPO_URL":     p.RepoURL,
			"DEJIMA_AGENT":        p.Agent,
			"DEJIMA_SOCKET":       "/run/dejima/dejimad.sock",
		},
		Volumes: []runtime.VolumeMount{
			{Name: p.WorkspaceVolume(), Target: "/workspace"},
			{Name: p.AgentVolume(), Target: "/home/dejima/.claude"},
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
		if err := s.createContainerForProject(r.Context(), p); err != nil {
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
	if err := s.createContainerForProject(r.Context(), p); err != nil {
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
			if stats, err := s.rt.Stats(ctx, p.ContainerName()); err == nil {
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
	return info
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
