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
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/islandimage"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/porttoken"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

const (
	// DefaultImage is the canonical island image. Built locally from image/Dockerfile.
	DefaultImage = "dejima/island:latest"
	// DefaultAgent is the agent run inside the island when none is specified.
	DefaultAgent = "claude-code"
	// AgentHeadless is the reserved agent type for islands that run a
	// user-provided command directly (no tmux, no interactive attach surface).
	// The command is supplied via CreateIslandRequest.Cmd → Project.Cmd →
	// DEJIMA_AGENT_CMD env var. Useful for API-SDK agents, background
	// workers, and anything that doesn't need an attach surface.
	AgentHeadless = "headless"
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

	// Per-(island,agent) last orchestration error (failed worktree/session
	// setup), surfaced in AgentInfo so failures aren't silent.
	agentErrMu  sync.Mutex
	agentErrors map[string]agentErrInfo

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

	// autonomyDial, when non-empty, is the host:port an in-island brain dials
	// to reach this daemon over the token-authenticated TCP path (the macOS
	// route where the unix socket can't be bind-mounted; e.g.
	// "host.docker.internal:7274"). Set via EnableAutonomy when dejimad's token
	// listener is enabled. When set, every provisioned container receives
	// DEJIMA_HOST plus its own per-island DEJIMA_TOKEN so the in-island CLI can
	// authenticate. Empty on the Linux/unix-socket path.
	autonomyDial string

	// sshAddr is the SSH-façade listen addr, recorded via EnableSSH purely so
	// /v1/overview can report it to clients. Empty unless dejimad has --ssh.
	sshAddr string

	// hostTerminals gates the operator host-terminal feature (uncontained shells
	// on the daemon host). Off unless dejimad is started with --host-terminals.
	// Even when on, the routes are operator-only and never reachable by an island
	// token (tokenauth default-deny).
	hostTerminals bool

	// reposFetch resolves the repositories an identity can browse. It defaults to
	// githubid.ListRepos (a live GitHub call); tests inject a stub so the handler
	// can be covered without reaching GitHub.
	reposFetch func(ctx context.Context, id githubid.Identity, limit int) (githubid.RepoList, error)

	startedAt time.Time
}

// EnableHostTerminals turns on the operator host-terminal feature. It exposes
// uncontained shells on the daemon host, so it is off by default and meant to be
// a deliberate operator opt-in (`dejimad --host-terminals`).
func (s *Server) EnableHostTerminals() { s.hostTerminals = true }

// HostTerminalsEnabled reports whether the host-terminal feature is on.
func (s *Server) HostTerminalsEnabled() bool { return s.hostTerminals }

// EnableAutonomy turns on the in-island → dejimad autonomy path: containers are
// provisioned with DEJIMA_HOST=dial and their per-island DEJIMA_TOKEN. dial is
// the address the container dials to reach this daemon (host-internal, e.g.
// host.docker.internal:<port>). Call only when the token listener is bound; an
// empty dial is a no-op.
func (s *Server) EnableAutonomy(dial string) { s.autonomyDial = dial }

// EnableSSH records the SSH-façade listen addr so clients (the TUI,
// `dejima ssh config/info`) can surface the connection target. Reporting only —
// the listener itself is owned by dejimad/main; this never opens a port.
func (s *Server) EnableSSH(addr string) { s.sshAddr = addr }

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
		agentErrors: map[string]agentErrInfo{},
		events_:     map[string][]events.Event{},
		eventsCap:   50,
		reposFetch:  githubid.ListRepos,
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
	s.agentStates[agentStateKey(e.Island, e.Agent)] = AgentStateInfo{Latest: short, UpdatedAt: e.Timestamp}
	s.agentStateMu.Unlock()
}

// agentStateKey is the composite map key for an (island, agent) agent-state.
func agentStateKey(island, agentID string) string {
	return island + "\x00" + agentID
}

// agentStateOf returns the latest agent-state entry for one agent, or nil.
func (s *Server) agentStateOf(island, agentID string) *AgentStateInfo {
	s.agentStateMu.Lock()
	defer s.agentStateMu.Unlock()
	if st, ok := s.agentStates[agentStateKey(island, agentID)]; ok {
		return &st
	}
	return nil
}

// agentErrInfo is a captured orchestration failure for one agent.
type agentErrInfo struct {
	Message string
	At      time.Time
}

// setAgentError records the last orchestration failure for an agent.
func (s *Server) setAgentError(island, agentID string, err error) {
	s.agentErrMu.Lock()
	s.agentErrors[agentStateKey(island, agentID)] = agentErrInfo{Message: err.Error(), At: time.Now().UTC()}
	s.agentErrMu.Unlock()
}

// clearAgentError drops any recorded failure for an agent (it came up cleanly).
func (s *Server) clearAgentError(island, agentID string) {
	s.agentErrMu.Lock()
	delete(s.agentErrors, agentStateKey(island, agentID))
	s.agentErrMu.Unlock()
}

// agentErrorOf returns the last recorded failure for an agent, if any.
func (s *Server) agentErrorOf(island, agentID string) (string, time.Time, bool) {
	s.agentErrMu.Lock()
	defer s.agentErrMu.Unlock()
	if e, ok := s.agentErrors[agentStateKey(island, agentID)]; ok {
		return e.Message, e.At, true
	}
	return "", time.Time{}, false
}

// islandAgentState returns the most recently updated agent-state across all of
// the island's agents — the island-level rollup signal.
func (s *Server) islandAgentState(island string) *AgentStateInfo {
	prefix := island + "\x00"
	s.agentStateMu.Lock()
	defer s.agentStateMu.Unlock()
	var best *AgentStateInfo
	for k, st := range s.agentStates {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if best == nil || st.UpdatedAt.After(best.UpdatedAt) {
			c := st
			best = &c
		}
	}
	return best
}

// Handler returns an http.Handler suitable for the daemon's listener.
// Handler returns the API handler for the fully-trusted listeners: the unix
// socket (filesystem-permission trust) and the tailnet-pinned TCP listener.
// Neither carries a per-request token; see TokenAuthHandler for the
// host-internal, token-authenticated autonomy path.
func (s *Server) Handler() http.Handler {
	return logMiddleware(s.log, s.routes())
}

// routes builds the route table shared by every listener. The differences
// between listeners live in the middleware that wraps this mux, never in the
// routes themselves, so there is exactly one source of truth for the API
// surface.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/islands", s.listIslands)
	mux.HandleFunc("POST /v1/islands", s.createIsland)
	mux.HandleFunc("GET /v1/islands/{name}", s.getIsland)
	mux.HandleFunc("DELETE /v1/islands/{name}", s.deleteIsland)
	mux.HandleFunc("PATCH /v1/islands/{name}", s.updateIsland)
	mux.HandleFunc("POST /v1/islands/{name}/hibernate", s.hibernateIsland)
	mux.HandleFunc("POST /v1/islands/{name}/wake", s.wakeIsland)
	mux.HandleFunc("POST /v1/islands/{name}/reset", s.resetIsland)
	mux.HandleFunc("POST /v1/islands/{name}/upgrade", s.upgradeIsland)
	mux.HandleFunc("POST /v1/image/build", s.handleImageBuild)
	mux.HandleFunc("POST /v1/admin/update", s.handleAdminUpdate)
	mux.HandleFunc("GET /v1/panic", s.handlePanicStatus)
	mux.HandleFunc("POST /v1/panic", s.handlePanic)
	mux.HandleFunc("DELETE /v1/panic", s.handleUnpanic)
	mux.HandleFunc("POST /v1/ssh/account-keys", s.handleAuthorizeAccountKey)
	mux.HandleFunc("GET /v1/ssh/account-keys", s.handleListAccountKeys)
	mux.HandleFunc("GET /v1/islands/{name}/session", s.sessionWS)
	mux.HandleFunc("GET /v1/islands/{name}/agents", s.listAgents)
	mux.HandleFunc("POST /v1/islands/{name}/agents", s.addAgent)
	mux.HandleFunc("GET /v1/islands/{name}/agents/{id}", s.getAgent)
	mux.HandleFunc("DELETE /v1/islands/{name}/agents/{id}", s.removeAgent)
	mux.HandleFunc("PATCH /v1/islands/{name}/agents/{id}", s.updateAgent)
	mux.HandleFunc("GET /v1/islands/{name}/agents/{id}/session", s.sessionWS)
	mux.HandleFunc("GET /v1/healthz", s.healthz)
	mux.HandleFunc("PUT /v1/credentials/claude", s.handlePushClaudeCreds)
	mux.HandleFunc("GET /v1/credentials/claude", s.handleClaudeCredsStatus)
	mux.HandleFunc("GET /v1/credentials/github", s.handleGitHubIdentities)
	mux.HandleFunc("PUT /v1/credentials/github/{name}", s.handlePutGitHubIdentity)
	mux.HandleFunc("DELETE /v1/credentials/github/{name}", s.handleDeleteGitHubIdentity)
	mux.HandleFunc("GET /v1/credentials/github/{name}/repos", s.handleGitHubRepos)
	mux.HandleFunc("GET /v1/events/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /v1/events/subscribe", s.subscribeWebhook)
	mux.HandleFunc("DELETE /v1/events/subscriptions/{id}", s.unsubscribeWebhook)
	mux.HandleFunc("POST /v1/internal/agent-event", s.handleAgentEvent)
	mux.HandleFunc("POST /v1/sessions/revoke", s.handleRevokeSessions)
	mux.HandleFunc("GET /v1/clients", s.handleClientHistory)
	mux.HandleFunc("GET /v1/overview", s.handleOverview)
	// Host terminals (operator-only, gated; never in tokenauth's allow-list).
	mux.HandleFunc("GET /v1/terminals", s.handleListTerminals)
	mux.HandleFunc("POST /v1/terminals", s.handleCreateTerminal)
	mux.HandleFunc("DELETE /v1/terminals/{id}", s.handleDeleteTerminal)
	mux.HandleFunc("PATCH /v1/terminals/{id}", s.handleRelabelTerminal)
	mux.HandleFunc("GET /v1/terminals/{id}/session", s.terminalSessionWS)
	mux.HandleFunc("GET /v1/islands/{name}/events", s.handleIslandEvents)
	mux.HandleFunc("POST /v1/islands/{name}/exec", s.handleExec)
	mux.HandleFunc("GET /v1/islands/{name}/files/{path...}", s.handleReadFile)
	mux.HandleFunc("PUT /v1/islands/{name}/files/{path...}", s.handleWriteFile)
	mux.HandleFunc("GET /v1/islands/{name}/logs", s.handleLogs)
	mux.HandleFunc("GET /v1/islands/{name}/port/scopes", s.handleListPortScopes)
	mux.HandleFunc("POST /v1/islands/{name}/port/scopes", s.handleGrantPortScope)
	mux.HandleFunc("DELETE /v1/islands/{name}/port/scopes/{scope}", s.handleRevokePortScope)
	mux.HandleFunc("POST /v1/islands/{name}/port/intake", s.handlePortIntake)
	mux.HandleFunc("POST /v1/islands/{name}/port/export", s.handlePortExport)
	mux.HandleFunc("POST /v1/islands/{name}/port/write", s.handlePortWrite)
	mux.HandleFunc("GET /v1/audit", s.handleAudit)
	return mux
}

// AdoptExisting brings the runtime state into alignment with persisted project
// state. Called at daemon startup. Best-effort: errors are logged but do not
// prevent the daemon from serving.
func (s *Server) AdoptExisting(ctx context.Context) {
	if panicEngaged() {
		s.log.Warn("adopt: PANIC flag set — leaving all islands stopped; `dejima panic --clear` to resume")
		return
	}
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
		// Restore non-primary agent sessions for islands meant to be running.
		if p.DesiredState == project.StateRunning {
			s.reconcileAgentsAsync(p)
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

// handleGitHubIdentities lists the daemon's GitHub identities (no tokens).
func (s *Server) handleGitHubIdentities(w http.ResponseWriter, _ *http.Request) {
	store, err := githubid.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, GitHubIdentitiesResponse{Identities: store.List()})
}

// handlePutGitHubIdentity adds or updates a named GitHub identity. This is how
// a credentialed client seeds the daemon (`dejima auth push --github`).
func (s *Server) handlePutGitHubIdentity(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("identity name is required"))
		return
	}
	var req PutGitHubIdentityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.Login) == "" || strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, errors.New("login and token are required"))
		return
	}
	store, err := githubid.Update(func(s *githubid.Store) error {
		s.Put(githubid.Identity{Name: name, Login: req.Login, Host: req.Host, Token: req.Token})
		if req.Default {
			_ = s.SetDefault(name)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("github identity stored", "name", name, "login", req.Login)
	writeJSON(w, http.StatusOK, GitHubIdentitiesResponse{Identities: store.List()})
}

// handleDeleteGitHubIdentity removes a GitHub identity.
func (s *Server) handleDeleteGitHubIdentity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var missing bool
	if _, err := githubid.Update(func(s *githubid.Store) error {
		missing = !s.Remove(name)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if missing {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such github identity %q", name))
		return
	}
	affected := s.islandsUsingIdentity(name)
	if len(affected) > 0 {
		s.log.Warn("deleted a github identity still referenced by islands",
			"name", name, "islands", affected)
	}
	writeJSON(w, http.StatusOK, DeleteGitHubIdentityResponse{AffectedIslands: affected})
}

// islandsUsingIdentity returns the names of islands that reference the named
// GitHub identity. Best-effort: a project-list failure yields no names rather
// than blocking the delete.
func (s *Server) islandsUsingIdentity(name string) []string {
	projects, err := project.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range projects {
		if p.GitHubIdentity == name {
			out = append(out, p.Name)
		}
	}
	return out
}

// handleGitHubRepos lists the repositories an identity can access, fetched
// daemon-side so any client device can browse without its own gh.
func (s *Server) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	store, err := githubid.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id, ok := store.Resolve(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such github identity %q", r.PathValue("name")))
		return
	}
	res, err := s.reposFetch(r.Context(), id, 100)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("list github repos: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, GitHubReposResponse{Repos: res.Repos, Capped: res.Capped})
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
	// Per-agent session liveness is detail-only (one exec per agent).
	info.Agents = s.agentInfos(r.Context(), p, info.Container == string(runtime.StatusRunning))
	// Git status is only computed in the detail view, not the list, because
	// it requires container exec and is the slowest field to populate.
	info.Git = s.gitStatusOf(r.Context(), p.ContainerName())
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

// agentsLive reports whether the island container is running (so callers know
// whether to probe per-agent session liveness).
func (s *Server) agentsLive(ctx context.Context, p *project.Project) bool {
	st, _ := s.rt.Status(ctx, p.ContainerName())
	return st == runtime.StatusRunning
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	p, err := project.Load(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, s.agentInfos(r.Context(), p, s.agentsLive(r.Context(), p)))
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, ok := p.AgentByID(id); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, id))
		return
	}
	for _, ai := range s.agentInfos(r.Context(), p, s.agentsLive(r.Context(), p)) {
		if ai.ID == id {
			writeJSON(w, http.StatusOK, ai)
			return
		}
	}
}

// addAgent adds an agent to an existing island. The agent gets its own git
// worktree + tmux session; if the island is running the session is brought up
// immediately, otherwise it materializes on the next wake (via reconcile).
// newAgentSpec validates an agent request and builds a non-primary AgentSpec
// with a freshly allocated id. Type defaults to the island's primary agent type
// (or DefaultAgent) when unset. Shared by addAgent and create-time seeding.
func (s *Server) newAgentSpec(p *project.Project, req AgentSpecRequest) (project.AgentSpec, error) {
	typ := strings.TrimSpace(req.Type)
	if typ == "" {
		if pa := p.PrimaryAgent(); pa != nil {
			typ = pa.Type
		} else {
			typ = DefaultAgent
		}
	}
	cmd := strings.TrimSpace(req.Cmd)
	if !handlers.Attachable(typ) && cmd == "" {
		// A headless handler with a baked Launch (e.g. openclaw) needs no cmd;
		// only a generic/custom headless type does.
		if h, ok := handlers.Lookup(typ); !ok || h.Launch == "" {
			return project.AgentSpec{}, fmt.Errorf("agent type %q is headless; it requires a command (cmd)", typ)
		}
	}
	if handlers.Attachable(typ) && cmd != "" {
		return project.AgentSpec{}, fmt.Errorf("cmd is only meaningful for headless agents, not %q", typ)
	}
	id := p.NextAgentID()
	spec := project.AgentSpec{
		ID:        id,
		Type:      typ,
		Label:     strings.TrimSpace(req.Label),
		Cmd:       cmd,
		Tmux:      "agent-" + id,
		Branch:    "agent/" + id,
		Worktree:  agentsWorktreeRoot + "/" + id,
		CreatedAt: time.Now().UTC(),
	}
	// A plain terminal pokes at the island's workspace directly — no isolated
	// worktree/branch, just a shell on /workspace.
	if typ == handlers.Shell {
		spec.Branch = ""
		spec.Worktree = "/workspace"
	}
	// Co-located headless agents self-restart by default so a crash doesn't end
	// the agent silently.
	if !handlers.Attachable(typ) {
		spec.Restart = true
	}
	return spec, nil
}

func (s *Server) addAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req AgentSpecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	spec, err := s.newAgentSpec(p, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := spec.ID
	typ := spec.Type
	p.AddAgent(spec)
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.agentsLive(r.Context(), p) {
		if err := s.ensureAgentSession(r.Context(), p, &p.Agents[len(p.Agents)-1]); err != nil {
			s.setAgentError(name, id, err)
			s.log.Warn("add agent: ensure session", "island", name, "agent", id, "err", err)
		} else {
			s.clearAgentError(name, id)
		}
	}
	s.emit(events.Event{
		Type:    events.TypeIslandAgentAdded,
		Island:  name,
		Agent:   id,
		Payload: map[string]any{"type": typ},
	})
	for _, ai := range s.agentInfos(r.Context(), p, s.agentsLive(r.Context(), p)) {
		if ai.ID == id {
			writeJSON(w, http.StatusCreated, ai)
			return
		}
	}
}

// removeAgent removes a non-primary agent from an island: kills its session and
// prunes its worktree (the branch is kept). The primary and the last remaining
// agent cannot be removed.
func (s *Server) removeAgent(w http.ResponseWriter, r *http.Request) {
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
	if len(p.Agents) <= 1 {
		writeError(w, http.StatusConflict, errors.New("cannot remove the last agent; purge the island instead"))
		return
	}
	if pa := p.PrimaryAgent(); pa != nil && pa.ID == id {
		writeError(w, http.StatusConflict, errors.New("cannot remove the primary agent"))
		return
	}
	if s.agentsLive(r.Context(), p) {
		s.removeAgentSession(r.Context(), p, a)
	}
	p.RemoveAgent(id)
	s.clearAgentError(name, id)
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{
		Type:   events.TypeIslandAgentRemoved,
		Island: name,
		Agent:  id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// updateIsland edits an island's cosmetic display title. Name and all infra
// identity (container, volumes, network, config dir) are immutable.
func (s *Server) updateIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req UpdateIslandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	p.Title = strings.TrimSpace(req.Title) // empty clears it
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// updateAgent changes an agent's cosmetic label. Everything else (id, type,
// worktree, session) is immutable — the id is the stable handle, the label is
// the renamable display name, mirroring the island Name / agent Label split.
func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
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
	var req AgentSpecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	a.Label = strings.TrimSpace(req.Label) // empty clears the label
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, ai := range s.agentInfos(r.Context(), p, s.agentsLive(r.Context(), p)) {
		if ai.ID == id {
			writeJSON(w, http.StatusOK, ai)
			return
		}
	}
}

// removeAgentSession kills an agent's tmux session and prunes its worktree dir.
// Best-effort; the worktree's branch is intentionally preserved.
func (s *Server) removeAgentSession(ctx context.Context, p *project.Project, a *project.AgentSpec) {
	if a.Tmux != "" {
		_, _, _, _ = s.rt.Exec(ctx, p.ContainerName(), []string{"tmux", "kill-session", "-t", a.Tmux})
	}
	if a.Worktree != "" && a.Worktree != "/workspace" {
		_, _, _, _ = s.rt.Exec(ctx, p.ContainerName(), []string{"git", "-C", "/workspace", "worktree", "remove", "--force", a.Worktree})
	}
}

func (s *Server) createIsland(w http.ResponseWriter, r *http.Request) {
	var req CreateIslandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	// A seed path is a valid clone source on its own — a local-copy of a repo
	// with no remote resolves to Repo="" + SeedPath set (origin stays unset).
	if strings.TrimSpace(req.Repo) == "" && strings.TrimSpace(req.SeedPath) == "" {
		writeError(w, http.StatusBadRequest, errors.New("repo is required (a URL, a local path, or a seed)"))
		return
	}
	name := req.Name
	if name == "" {
		src := req.Repo
		if src == "" {
			src = req.SeedPath // no-remote local copy: derive the name from the seed dir
		}
		name = project.DeriveNameFromRepo(src)
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

	// Agents, when present, is the source of truth; element 0 defines the primary.
	// Otherwise fall back to the scalar agent/cmd (single-agent back-compat).
	agent := strings.TrimSpace(req.Agent)
	cmd := strings.TrimSpace(req.Cmd)
	if len(req.Agents) > 0 {
		primary := req.Agents[0]
		agent = strings.TrimSpace(primary.Type)
		cmd = strings.TrimSpace(primary.Cmd)
	}
	if agent == "" {
		agent = DefaultAgent
	}
	image := req.Image
	if image == "" {
		image = DefaultImage
	}
	if agent == AgentHeadless && cmd == "" {
		writeError(w, http.StatusBadRequest, errors.New(`agent "headless" requires a non-empty cmd`))
		return
	}
	if agent != AgentHeadless && cmd != "" {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("cmd is only meaningful with agent=%q", AgentHeadless))
		return
	}
	switch req.Role {
	case project.RoleProject, project.RoleHome:
		// ok
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid role %q (want %q or %q)", req.Role, project.RoleProject, project.RoleHome))
		return
	}
	if req.Role == project.RoleHome && agent != AgentHeadless {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("a home island runs an assistant brain — it must be headless (agent=%q with a cmd)", AgentHeadless))
		return
	}
	// A named GitHub identity must already exist on the daemon (an empty value
	// is fine — it resolves to the default, or the host gh).
	if gid := strings.TrimSpace(req.GitHubIdentity); gid != "" {
		store, err := githubid.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("load github identities: %w", err))
			return
		}
		if _, ok := store.Resolve(gid); !ok {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown github identity %q (see `dejima auth status`)", gid))
			return
		}
	}
	// Validate extra seed agents up front so bad input is a clean 400, not a
	// provisioning 500. Element 0 is the primary, already validated above.
	for i, a := range req.Agents {
		if i == 0 {
			continue
		}
		t := strings.TrimSpace(a.Type)
		if t == "" {
			t = agent
		}
		ac := strings.TrimSpace(a.Cmd)
		if !handlers.Attachable(t) && ac == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("agent %d (%q) is headless; it requires a cmd", i, t))
			return
		}
		if handlers.Attachable(t) && ac != "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("agent %d (%q): cmd is only meaningful for headless agents", i, t))
			return
		}
	}

	p, err := s.provision(r.Context(), name, req.Repo, agent, image, cmd, req.Role, req.GitHubIdentity, req.Resources, req.SeedPath, req.Agents)
	if err != nil {
		// Best-effort cleanup: remove anything we created if provisioning failed mid-flight.
		s.log.Error("provision failed; cleaning up", "name", name, "err", err)
		_ = s.teardown(context.Background(), p, true)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.emit(events.Event{Type: events.TypeIslandCreated, Island: p.Name})
	s.emit(events.Event{Type: events.TypeIslandRunning, Island: p.Name})

	resp := CreateIslandResponse{IslandInfo: s.toInfo(r.Context(), p)}
	// Parent-child spawn: when a Home Island created this island over the
	// token-authenticated path, hand back the child's token so the parent brain
	// can drive it. authorizeToken already restricts create to Home tokens, so a
	// non-empty token-island here is necessarily a Home parent. Operator creates
	// have no token-island and get no token in the response.
	if parent := TokenIslandFromContext(r.Context()); parent != "" {
		tok, err := porttoken.Ensure(p.Name)
		if err != nil {
			s.log.Error("mint child token", "child", p.Name, "parent", parent, "err", err)
			writeError(w, http.StatusInternalServerError, fmt.Errorf("island created but child token unavailable: %w", err))
			return
		}
		resp.Token = tok
		s.log.Info("home spawned child island", "child", p.Name, "parent", parent)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) deleteIsland(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	force := r.URL.Query().Get("force") == "true"
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !force {
		if riskErr := s.purgeRiskError(r.Context(), p); riskErr != nil {
			writeError(w, http.StatusConflict, riskErr)
			return
		}
	}
	if err := s.teardown(r.Context(), p, true); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandPurged, Island: p.Name})
	w.WriteHeader(http.StatusNoContent)
}

// purgeRiskError returns a non-nil error describing at-risk work in an island's
// workspace when purging without --force would be unsafe, or nil when there is
// nothing worth guarding. Purge removes the workspace volume, so any uncommitted
// or unpushed git work in /workspace is destroyed unrecoverably.
//
// When the container is running we inspect /workspace directly. When it is not
// running we cannot verify (the workspace lives in a Docker volume we don't exec
// into), so we fail safe: ask the operator to wake it or pass --force.
func (s *Server) purgeRiskError(ctx context.Context, p *project.Project) error {
	status, _ := s.rt.Status(ctx, p.ContainerName())
	if status != runtime.StatusRunning {
		return fmt.Errorf("island %q is not running, so its workspace can't be checked for "+
			"uncommitted or unpushed work; wake it (`dejima wake %s`) to let the guard verify, "+
			"or re-run with --force to purge anyway", p.Name, p.Name)
	}
	git := s.gitStatusOf(ctx, p.ContainerName())
	if git == nil {
		// /workspace isn't a git repo (or the check failed) — nothing git-tracked
		// to lose. Allow the purge.
		return nil
	}
	var risks []string
	if !git.Clean && git.DirtyFiles > 0 {
		risks = append(risks, countNoun(git.DirtyFiles, "uncommitted change"))
	}
	if git.Ahead > 0 {
		risks = append(risks, countNoun(git.Ahead, "unpushed commit"))
	}
	if len(risks) == 0 {
		return nil
	}
	branch := git.Branch
	if branch == "" {
		branch = "HEAD"
	}
	return fmt.Errorf("island %q has %s on branch %s — purging destroys it permanently; "+
		"commit/push first, or re-run with --force to purge anyway",
		p.Name, strings.Join(risks, " and "), branch)
}

// countNoun renders a count with a singular/plural noun: countNoun(1, "commit")
// → "1 commit", countNoun(3, "commit") → "3 commits".
func countNoun(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// provision creates the on-disk project, volumes, and a running container.
// seedAgents, when non-empty, describes the island's agents (element 0 is the
// primary, already synthesized from the scalar agent/cmd); the rest are added
// as co-located agents before the container is reconciled.
func (s *Server) provision(ctx context.Context, name, repo, agent, image, cmd, role, ghIdentity string, res Resources, seedPath string, seedAgents []AgentSpecRequest) (*project.Project, error) {
	exists, err := s.rt.ImageExists(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("check image %s: %w", image, err)
	}
	if !exists {
		return nil, fmt.Errorf("image %s not found locally; build it with `dejima image build`", image)
	}

	now := time.Now().UTC()
	p := &project.Project{
		Name:           name,
		RepoURL:        repo,
		Agent:          agent,
		Image:          image,
		Cmd:            cmd,
		Role:           role,
		GitHubIdentity: ghIdentity,
		Resources: project.Resources{
			Memory: res.Memory,
			CPUs:   res.CPUs,
			Disk:   res.Disk,
		},
		DesiredState: project.StateRunning,
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	p.EnsureAgents()                             // mirror the scalar agent into Agents[0] for new islands
	p.SetPrimaryID(project.PrimaryAgentID(name)) // fresh island: island-letter primary id (p1), not the legacy a1 back-fill
	// Seed any additional agents requested at create time. Agents[0] is the
	// primary (just synthesized); apply its label and add the rest as co-located
	// agents — reconcileAgents brings up their worktrees + sessions below.
	if len(seedAgents) > 0 {
		if lbl := strings.TrimSpace(seedAgents[0].Label); lbl != "" {
			p.Agents[0].Label = lbl
		}
		for _, ar := range seedAgents[1:] {
			spec, err := s.newAgentSpec(p, ar)
			if err != nil {
				return p, err
			}
			p.AddAgent(spec)
		}
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
	if err := s.rt.EnsureVolume(ctx, p.HomeVolume()); err != nil {
		return p, fmt.Errorf("create home volume: %w", err)
	}
	if err := s.rt.EnsureNetwork(ctx, p.NetworkName()); err != nil {
		return p, fmt.Errorf("create network: %w", err)
	}

	if err := s.createContainerForProject(ctx, p, seedPath); err != nil {
		return p, err
	}
	s.reconcileAgentsAsync(p) // bring up any non-primary agents once the clone lands
	return p, nil
}

// createContainerForProject creates the long-lived container for an existing
// project. Used by provision() and reset().
func (s *Server) createContainerForProject(ctx context.Context, p *project.Project, seedPath string) error {
	binds, err := credentialBindMounts(p)
	if err != nil {
		return err
	}

	// A local-copy seed: mount the host repo read-only so the island can clone
	// from it into its own workspace volume (the silo stays an independent copy).
	// Only meaningful at first provision; the workspace persists across recreate.
	env := map[string]string{
		"DEJIMA_PROJECT_NAME": p.Name,
		"DEJIMA_REPO_URL":     p.RepoURL,
	}
	// A Home Island hosts an assistant brain; let it self-identify so it can
	// drive the Port (intake/export) and spawn work islands via the daemon API.
	if p.IsHome() {
		env["DEJIMA_HOME"] = "1"
	}
	// The in-island → dejimad path is the token-authenticated host-internal TCP
	// listener (DEJIMA_HOST + per-island DEJIMA_TOKEN). It carries both the
	// agent-event telemetry (notify.sh hooks) and the Home-island autonomy
	// surface. The daemon's control socket is NOT mounted into containers, so
	// this token — island-scoped in tokenauth.go — is the only way in, and it
	// only reaches the island's own surface. autonomyDial is empty only when the
	// token listener failed to bind; telemetry then degrades to a no-op.
	if s.autonomyDial != "" {
		tok, err := porttoken.Ensure(p.Name)
		if err != nil {
			return fmt.Errorf("mint island token: %w", err)
		}
		env["DEJIMA_HOST"] = s.autonomyDial
		env["DEJIMA_TOKEN"] = tok
	}
	// Everything the entrypoint needs about the primary agent flows via env, so
	// the launch command lives in one place (the handler registry) rather than
	// being duplicated in start.sh. Non-primary agents are launched by the daemon
	// (reconcileAgents), each overriding DEJIMA_AGENT_ID per session.
	agentType := p.Agent
	if pa := p.PrimaryAgent(); pa != nil {
		agentType = pa.Type
		env["DEJIMA_AGENT_ID"] = pa.ID
		env["DEJIMA_TMUX"] = pa.Tmux
		if pa.Cmd != "" {
			env["DEJIMA_AGENT_CMD"] = pa.Cmd
		}
		if h, ok := handlers.Lookup(pa.Type); ok {
			env["DEJIMA_LAUNCH"] = h.Launch // empty for headless → entrypoint runs DEJIMA_AGENT_CMD as PID 1
		}
	}
	env["DEJIMA_AGENT"] = agentType
	if seedPath != "" {
		binds = append(binds, runtime.BindMount{
			HostPath:      seedPath,
			ContainerPath: "/opt/host/seed",
			ReadOnly:      true,
		})
		env["DEJIMA_SEED"] = "/opt/host/seed"
	}

	// The daemon's control socket is deliberately NOT mounted into the container:
	// it is the operator's full-control plane, and mounting it would let in-island
	// code reach the entire API (create/delete islands, grant Port scopes, …).
	// In-island callers reach the daemon only over the token-authenticated,
	// island-scoped TCP path (DEJIMA_HOST above). The route to that host-internal
	// listener needs host.docker.internal to resolve: built in on Docker Desktop /
	// colima; add-host wires it on engines that don't provide it.
	var extraHosts []string
	if s.autonomyDial != "" {
		extraHosts = append(extraHosts, "host.docker.internal:host-gateway")
	}

	req := runtime.CreateRequest{
		Name:  p.ContainerName(),
		Image: p.Image,
		Env:   env,
		Volumes: []runtime.VolumeMount{
			{Name: p.WorkspaceVolume(), Target: "/workspace"},
			// The whole home is one per-island volume shared by every agent, so
			// tool auth persists and is shared across agents (see HomeVolume).
			{Name: p.HomeVolume(), Target: "/home/dejima"},
		},
		BindMounts:  binds,
		ExtraHosts:  extraHosts,
		Memory:      p.Resources.Memory,
		CPUs:        p.Resources.CPUs,
		StorageSize: p.Resources.Disk,
		Network:     p.NetworkName(),
		Labels: map[string]string{
			"dejima.project": p.Name,
			"dejima.agent":   agentType,
		},
	}
	if _, err := s.rt.CreateContainer(ctx, req); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// agentsWorktreeRoot is where non-primary agents' git worktrees live inside the
// island's workspace.
const agentsWorktreeRoot = "/workspace/.agents"

// reconcileAgentsAsync ensures the island's non-primary agent sessions exist, in
// the background. The container entrypoint launches the primary agent; the
// daemon owns the rest. Safe to call after create, wake, and at adopt.
func (s *Server) reconcileAgentsAsync(p *project.Project) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.reconcileAgents(ctx, p); err != nil {
			s.log.Warn("reconcile agents", "island", p.Name, "err", err)
		}
	}()
}

// reconcileAgents brings tmux sessions and worktrees into line with p.Agents for
// every non-primary agent. Idempotent. The primary (Agents[0]) is launched by
// the entrypoint and skipped here.
func (s *Server) reconcileAgents(ctx context.Context, p *project.Project) error {
	if len(p.Agents) <= 1 {
		return nil
	}
	if !s.waitForWorkspace(ctx, p) {
		return fmt.Errorf("workspace not ready for %q", p.Name)
	}
	for i := 1; i < len(p.Agents); i++ {
		a := &p.Agents[i]
		if err := s.ensureAgentSession(ctx, p, a); err != nil {
			s.setAgentError(p.Name, a.ID, err)
			s.log.Warn("ensure agent session", "island", p.Name, "agent", a.ID, "err", err)
		} else {
			s.clearAgentError(p.Name, a.ID)
		}
	}
	return nil
}

// waitForWorkspace blocks until the island's repo clone lands /workspace/.git,
// or a bounded deadline passes. RepoURL is empty for a no-remote local-copy
// (which still clones a real repo from the seed), so we cannot gate on it — we
// poll for .git, then proceed once it appears or the deadline passes (a
// genuinely repo-less island never grows a .git and ensureWorktree falls back to
// /workspace). Returns false only if ctx is cancelled.
func (s *Server) waitForWorkspace(ctx context.Context, p *project.Project) bool {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"test", "-e", "/workspace/.git"}); err == nil && code == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return true // no repo materialized; proceed and let ensureWorktree cope
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}

// ensureAgentSession makes sure one non-primary agent has its worktree and a
// running tmux session. Idempotent.
func (s *Server) ensureAgentSession(ctx context.Context, p *project.Project, a *project.AgentSpec) error {
	wt := a.Worktree
	if wt == "" {
		wt = "/workspace"
	}
	if wt != "/workspace" {
		if err := s.ensureWorktree(ctx, p, a, wt); err != nil {
			s.log.Warn("ensure worktree; falling back to /workspace", "island", p.Name, "agent", a.ID, "err", err)
			wt = "/workspace"
		}
	}
	if a.Tmux == "" {
		return fmt.Errorf("agent %q has no tmux session name", a.ID)
	}
	if ok, _ := s.tmuxHasSession(ctx, p, a.Tmux); ok {
		return nil
	}
	// Both interactive and headless agents run inside a tmux session (the host
	// process), scoped to DEJIMA_AGENT_ID via sh so we don't depend on a specific
	// tmux version's `new-session -e`. Headless agents are marked non-attachable,
	// redirect to a per-agent log file, and optionally restart on crash.
	script := agentLaunchScript(a)
	_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{
		"tmux", "new-session", "-d", "-s", a.Tmux, "-c", wt, "sh", "-c", script,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("tmux new-session for %q: %s", a.ID, strings.TrimSpace(stderr))
	}
	return nil
}

// headlessLogPath is the per-agent log file for a co-located headless agent.
func headlessLogPath(agentID string) string {
	return "/home/dejima/.dejima/agents/" + agentID + ".log"
}

// agentLaunchScript builds the sh -c script that a tmux session runs for one
// agent. Interactive agents exec their launch command directly; headless agents
// redirect output to a per-agent log file and (when Restart) self-respawn.
func agentLaunchScript(a *project.AgentSpec) string {
	idEnv := "DEJIMA_AGENT_ID=" + a.ID + " "
	if handlers.Attachable(a.Type) {
		h, _ := handlers.Lookup(a.Type)
		launch := h.Launch
		if launch == "" {
			launch = a.Type // unknown/custom interactive agent: run the type as a command
		}
		return idEnv + "exec " + launch
	}
	// Headless: capture output to the per-agent log, optionally with a restart loop.
	cmd := a.Cmd
	if cmd == "" {
		// A headless handler may bake its launch (e.g. openclaw); otherwise run
		// the type string as a command (generic/custom headless agents).
		if h, ok := handlers.Lookup(a.Type); ok && h.Launch != "" {
			cmd = h.Launch
		} else {
			cmd = a.Type
		}
	}
	log := headlessLogPath(a.ID)
	if a.Restart {
		return fmt.Sprintf("exec >> %s 2>&1; while true; do %s%s; echo \"[dejima] agent %s exited ($?); restarting in 3s\"; sleep 3; done",
			log, idEnv, cmd, a.ID)
	}
	return fmt.Sprintf("exec >> %s 2>&1; %s%s", log, idEnv, cmd)
}

// ensureWorktree creates the agent's git worktree if absent. Idempotent.
func (s *Server) ensureWorktree(ctx context.Context, p *project.Project, a *project.AgentSpec, wt string) error {
	if _, _, code, _ := s.rt.Exec(ctx, p.ContainerName(), []string{"test", "-e", wt + "/.git"}); code == 0 {
		return nil // already a worktree
	}
	if _, _, code, _ := s.rt.Exec(ctx, p.ContainerName(), []string{"test", "-e", "/workspace/.git"}); code != 0 {
		return fmt.Errorf("no repo at /workspace to base a worktree on")
	}
	_, _, _, _ = s.rt.Exec(ctx, p.ContainerName(), []string{"mkdir", "-p", agentsWorktreeRoot})
	branch := a.Branch
	if branch == "" {
		branch = "agent/" + a.ID
	}
	// Try a fresh branch; if it already exists, attach to it instead.
	if _, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"git", "-C", "/workspace", "worktree", "add", wt, "-b", branch}); err == nil && code == 0 {
		return nil
	}
	_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"git", "-C", "/workspace", "worktree", "add", wt, branch})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(stderr))
	}
	return nil
}

// tmuxHasSession reports whether a tmux session exists in the island container.
func (s *Server) tmuxHasSession(ctx context.Context, p *project.Project, session string) (bool, error) {
	if session == "" {
		return false, nil
	}
	_, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"tmux", "has-session", "-t", session})
	return err == nil && code == 0, err
}

// teardown removes the container, volumes, network, and on-host config dir.
func (s *Server) teardown(ctx context.Context, p *project.Project, force bool) error {
	if p == nil {
		return nil
	}
	_ = s.rt.RemoveContainer(ctx, p.ContainerName(), force)
	_ = s.rt.RemoveVolume(ctx, p.WorkspaceVolume(), force)
	_ = s.rt.RemoveVolume(ctx, p.HomeVolume(), force)
	_ = s.rt.RemoveNetwork(ctx, p.NetworkName())
	// Drop the island's materialized GitHub identity (a plaintext token on disk);
	// it lives outside the project dir, so project.Delete won't catch it.
	if dir, err := paths.GitHubIslandConfigPath(p.Name); err == nil {
		_ = os.RemoveAll(dir)
	}
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
	s.reconcileAgentsAsync(p) // the entrypoint relaunches the primary; restore the rest
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

	// Clear the shared home-state volume (agent creds + tool auth); the workspace
	// is preserved.
	if err := s.rt.RemoveVolume(r.Context(), p.HomeVolume(), true); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("remove home volume: %w", err))
		return
	}
	if err := s.rt.EnsureVolume(r.Context(), p.HomeVolume()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("recreate home volume: %w", err))
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
	// The island's headline agent/cmd is the primary agent's (Agents is the
	// source of truth; the scalar Project fields are just its input form).
	agentType, cmd := p.Agent, p.Cmd
	if pa := p.PrimaryAgent(); pa != nil {
		agentType, cmd = pa.Type, pa.Cmd
	}
	info := IslandInfo{
		Name:       p.Name,
		Title:      p.Title,
		Repo:       p.RepoURL,
		Agent:      agentType,
		Image:      p.Image,
		Cmd:        cmd,
		Role:       p.Role,
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
	info.Attached = s.islandPresence(p.Name)
	info.AgentState = s.islandAgentState(p.Name)
	info.Agents = s.agentInfos(ctx, p, false)
	return info
}

// agentInfos builds the per-agent public view. When live is true, each agent's
// tmux session liveness is probed (one container exec per agent) — detail-only,
// since the list view refreshes frequently and would multiply the exec cost.
func (s *Server) agentInfos(ctx context.Context, p *project.Project, live bool) []AgentInfo {
	out := make([]AgentInfo, 0, len(p.Agents))
	for i := range p.Agents {
		a := &p.Agents[i]
		ai := AgentInfo{
			ID:         a.ID,
			Type:       a.Type,
			Label:      a.Label,
			Tmux:       a.Tmux,
			Branch:     a.Branch,
			Worktree:   a.Worktree,
			Attachable: handlers.Attachable(a.Type),
			CreatedAt:  a.CreatedAt,
		}
		if live && a.Tmux != "" {
			if ok, _ := s.tmuxHasSession(ctx, p, a.Tmux); ok {
				ai.State = "running"
			} else {
				ai.State = "stopped"
			}
		}
		ai.Attached = s.presenceSnapshot(p.Name, a.ID)
		ai.AgentState = s.agentStateOf(p.Name, a.ID)
		if ai.AgentState == nil && i == 0 {
			// Surface legacy agent-less events (no DEJIMA_AGENT_ID) on the primary.
			ai.AgentState = s.agentStateOf(p.Name, "")
		}
		if msg, at, ok := s.agentErrorOf(p.Name, a.ID); ok {
			ai.Error, ai.ErrorAt = msg, at
		}
		out = append(out, ai)
	}
	return out
}

// credentialBindMounts assembles the host paths to mount read-only into the island.
// Missing paths are silently skipped so users without `gh` configured can still init.
// islandGHConfigDir resolves the island's GitHub identity (its chosen name, or
// the daemon default) and materializes a single-identity gh config dir for it,
// returning the host dir to mount read-only at /opt/host/gh-config. Returns ""
// (no error) when the store resolves no identity, so the caller falls back to
// the host's own ~/.config/gh.
func islandGHConfigDir(p *project.Project) (string, error) {
	store, err := githubid.Load()
	if err != nil {
		return "", fmt.Errorf("load github identities: %w", err)
	}
	id, ok := store.Resolve(p.GitHubIdentity)
	if !ok {
		return "", nil
	}
	dir, err := paths.GitHubIslandConfigDir(p.Name)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(githubid.HostsYAML(id)), 0o600); err != nil {
		return "", fmt.Errorf("write island gh config: %w", err)
	}
	return dir, nil
}

func credentialBindMounts(p *project.Project) ([]runtime.BindMount, error) {
	var binds []runtime.BindMount

	// GitHub: a per-island identity (chosen at create time, or the daemon
	// default) materializes its own single-identity gh config and overrides the
	// shared host mount. Falls back to the host's ~/.config/gh when the store
	// resolves no identity — so islands keep working before any are configured.
	if dir, err := islandGHConfigDir(p); err != nil {
		return nil, err
	} else if dir != "" {
		binds = append(binds, runtime.BindMount{
			HostPath: dir, ContainerPath: "/opt/host/gh-config", ReadOnly: true,
		})
	} else if ghDir, err := paths.HostGHConfigDir(); err == nil {
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
