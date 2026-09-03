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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/capability"
	"github.com/aoos/dejima/internal/egress"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/islandimage"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/mailbox"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/porttoken"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/providercreds"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/secrets"
	"github.com/aoos/dejima/internal/spawn"
	"github.com/aoos/dejima/internal/usage"
	"github.com/aoos/dejima/internal/version"
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

	// Graceful-restart machinery. restartMu guards the live-session-websocket
	// registry and the restarting flag. On daemon shutdown,
	// CloseSessionsForRestart sets restarting and closes every attached session
	// websocket with a reconnect-triggering close code (Service Restart, 1012),
	// so clients re-dial and resume the still-running in-island tmux instead of
	// reading the shutdown as a deliberate detach. restarting also makes the
	// session pumps SKIP their normal "deliberate end" signals (the handler's
	// NormalClosure and the {"type":"exit"} PTY-error envelope) so the 1012 close
	// wins the race — otherwise a restart would look like an exit and drop the
	// terminal instead of reconnecting.
	restartMu    sync.Mutex
	restarting   bool
	sessionConns map[*sessionConnHandle]*websocket.Conn
	events       *events.Manager
	mailbox      *mailbox.Store // intra-island agent message ring (Lane 5, Phase 1)
	linkQueue    *link.Queue    // pending cross-island action approvals (Lane 5, Phase 3; in-memory, fail-closed)

	// Wake-on-message (Lane 5, Phase 3.5). wakeEnabled gates the default
	// soft-notify; wakeNudges batches pending notifications. injectFn/idleFn are
	// the session-inject + turn-boundary seams (swapped in tests).
	wakeEnabled bool
	wakeNudges  *wakeNotifier
	injectFn    func(ctx context.Context, p *project.Project, a *project.AgentSpec, text string) error
	idleFn      func(island, agent string) bool

	// Claude credential auto-seed (see claude_autoseed.go). autoSeed guards the
	// one-shot capture so it runs at most once per boot and short-circuits cheaply
	// on the steady-state (already-seeded) path.
	autoSeed autoSeedState

	// In-memory ring buffer of recent attach/detach events. Bounded so the
	// daemon never accumulates client history indefinitely. Not persisted —
	// daemon restart loses it. Surveillance-free by design.
	historyMu   sync.Mutex
	historyRing []ClientHistoryEntry
	historyCap  int

	// Per-island latest agent state, derived from agent-event hooks.
	agentStateMu sync.Mutex
	agentStates  map[string]AgentStateInfo

	// Per-(island,agent) latest adapter-reported token/cost usage, derived from
	// agent.usage hooks (Claude Code today). Surfaced on AgentInfo.Usage.
	agentUsageMu sync.Mutex
	agentUsage   map[string]AgentUsage

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

	// Volume-size cache. `docker system df -v` is slower than `docker stats`
	// and disk size moves slowly, so this is cached with a longer TTL than
	// statsData and only consulted on the detail endpoint.
	diskMu   sync.Mutex
	diskData map[string]int64
	diskAt   time.Time

	// autonomyDial, when non-empty, is the host:port an in-island brain dials
	// to reach this daemon over the token-authenticated TCP path (the macOS
	// route where the unix socket can't be bind-mounted; e.g.
	// "host.docker.internal:7274"). Set via EnableAutonomy when dejimad's token
	// listener is enabled. When set, every provisioned container receives
	// DEJIMA_HOST plus its own per-island DEJIMA_TOKEN so the in-island CLI can
	// authenticate. Empty on the Linux/unix-socket path.
	autonomyDial string

	// egressDial / egressLog gate the island egress proxy (Phase 1, observe-
	// first). When egressDial is non-empty (the host:port islands dial to reach
	// the daemon's egress proxy, e.g. "host.docker.internal:7280"), every new
	// container gets HTTPS_PROXY pointed at it and the proxy records each
	// destination into egressLog (served by GET /v1/islands/{name}/egress). Both
	// zero when the proxy is disabled (the default) — islands then have direct
	// egress, unchanged. Set via EnableEgress; the listener is owned by
	// dejimad/main.
	egressDial   string
	egressLog    *egress.Log
	egressPolicy *egress.PolicyStore // Phase 2: per-island allow/deny policy (operator-set; the proxy enforces it)

	// sshAddr is the SSH-façade listen addr, recorded via EnableSSH purely so
	// /v1/overview can report it to clients. Empty unless dejimad has --ssh.
	sshAddr string
	// sshHostKey is the façade's host public key (OpenSSH authorized-key line),
	// reported alongside sshAddr so a client can pin it in a known_hosts file it
	// manages itself — making a rotated host key self-heal.
	sshHostKey string

	// hostTerminals gates the operator host-terminal feature (uncontained shells
	// on the daemon host). Off unless dejimad is started with --host-terminals.
	// Even when on, the routes are operator-only and never reachable by an island
	// token (tokenauth default-deny).
	hostTerminals bool

	// requireToken, when set, makes the operator surface reject anonymous
	// (no-token) requests instead of treating them as the trusted owner — see
	// roleauth.go. Off by default; turned on via RequireToken (dejimad
	// --require-token). The team-auth role/scope attenuation in roleauth.go
	// applies to every presented token regardless of this flag.
	requireToken bool

	// reposFetch resolves the repositories an identity can browse. It defaults to
	// githubid.ListRepos (a live GitHub call); tests inject a stub so the handler
	// can be covered without reaching GitHub.
	reposFetch func(ctx context.Context, id githubid.Identity, limit int) (githubid.RepoList, error)

	// anonCloneFn probes whether a repo URL is reachable WITHOUT credentials (the
	// public-repo check behind the create-time identity gate). Defaults to
	// repoAnonCloneable (a real git ls-remote); tests inject a stub so the gate is
	// covered without network.
	anonCloneFn func(ctx context.Context, url string) bool

	// GitHub device-flow credential capture. githubClientID is the Dejima OAuth
	// app's public client id (DEJIMAD_GITHUB_CLIENT_ID); empty ⇒ device flow is
	// dark and the PAT path is the only route. The two Fn fields are the GitHub
	// calls, injectable so the flow is tested without reaching GitHub.
	githubClientID  string
	deviceSessions  *deviceSessions
	ghDeviceStartFn func(ctx context.Context, clientID string) (deviceStartResult, error)
	ghDevicePollFn  func(ctx context.Context, clientID, deviceCode string) (devicePollResult, error)

	// capAdapter runs capability targets (the broker's execution half). nil ⇒
	// resolve per host OS via capability.DefaultAdapter on demand; tests inject one.
	capAdapter capability.Adapter

	// auditEnabled gates the OPERATIONAL audit log: api.request + lifecycle
	// records appended to the hash-chained ledger. Off by default; turned on by
	// EnableAudit (dejimad --audit). The brokered-operation records (port.*,
	// trade.*, capability.*) are always written regardless — this only governs
	// the extra operational layer. See internal/api/audit.go.
	auditEnabled bool
	// auditReads, when set, also records read (GET) requests; the default records
	// state-changing requests only (mutations + lifecycle), keeping the log lean
	// and free of high-volume TUI polling noise.
	auditReads bool

	startedAt time.Time
}

// capabilityAdapter returns the host's capability execution adapter, or an error
// when this host has none yet (e.g. macOS until the Shortcuts adapter ships).
func (s *Server) capabilityAdapter() (capability.Adapter, error) {
	if s.capAdapter != nil {
		return s.capAdapter, nil
	}
	return capability.DefaultAdapter()
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

// EnableEgress wires the island egress proxy: dial is the host:port islands
// reach the proxy at (injected as HTTPS_PROXY into new containers), log is where
// the proxy records destinations and the read API serves from, and policy is the
// per-island allow/deny store the operator mutates via the API (the same store
// the proxy enforces). Off by default; dejimad/main owns the proxy listener.
func (s *Server) EnableEgress(dial string, log *egress.Log, policy *egress.PolicyStore) {
	s.egressDial = dial
	s.egressLog = log
	s.egressPolicy = policy
}

// EnableSSH records the SSH-façade listen addr so clients (the TUI,
// `dejima ssh config/info`) can surface the connection target. Reporting only —
// the listener itself is owned by dejimad/main; this never opens a port.
func (s *Server) EnableSSH(addr, hostKey string) { s.sshAddr, s.sshHostKey = addr, hostKey }

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

// volumeSizes returns on-disk size (bytes) per volume name, cached for 30s
// because `docker system df -v` is slow and disk usage drifts slowly. Returns
// the last good sample (possibly nil) on error.
func (s *Server) volumeSizes(ctx context.Context) map[string]int64 {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if s.diskData != nil && time.Since(s.diskAt) < 30*time.Second {
		return s.diskData
	}
	data, err := s.rt.VolumeSizes(ctx)
	if err != nil {
		return s.diskData
	}
	s.diskData, s.diskAt = data, time.Now()
	return data
}

// NewServer constructs a server backed by the given runtime.
// openMailbox returns the intra-island mailbox store, persisted to
// ~/.dejima/mailbox.json so undelivered messages + the seq cursor survive a
// daemon restart (self-update, crash, colima resize). If the state dir can't be
// resolved it degrades to an in-memory store — messaging still works, just not
// across a restart.
func openMailbox(log *slog.Logger) *mailbox.Store {
	root, err := paths.Root()
	if err != nil {
		if log != nil {
			log.Warn("mailbox: no state dir; messages won't survive a daemon restart", "err", err)
		}
		return mailbox.NewStore(256)
	}
	return mailbox.Open(filepath.Join(root, "mailbox.json"), 256, log)
}

func NewServer(rt runtime.Runtime, log *slog.Logger, ev *events.Manager) *Server {
	s := &Server{
		rt:          rt,
		log:         log,
		locks:       map[string]*sync.Mutex{},
		presence:    map[string]*presenceTracker{},
		events:      ev,
		mailbox:     openMailbox(log),
		linkQueue:   link.NewQueue(15 * time.Minute),
		wakeEnabled: true, // default soft-notify on, so it works with no wrapper
		wakeNudges:  newWakeNotifier(),
		historyCap:  200,
		agentStates: map[string]AgentStateInfo{},
		agentUsage:  map[string]AgentUsage{},
		agentErrors: map[string]agentErrInfo{},
		events_:     map[string][]events.Event{},
		eventsCap:   50,
		reposFetch:  githubid.ListRepos,
		anonCloneFn: repoAnonCloneable,
		startedAt:   time.Now().UTC(),
	}
	// Wake-on-message seams (swappable in tests) + the store's arrival hook.
	s.injectFn = s.tmuxInject
	s.idleFn = s.agentIdleAtBoundary
	s.mailbox.SetArrivalHook(s.onMailboxArrival)
	// GitHub device-flow capture: real GitHub calls by default (tests stub them);
	// client id from env, empty ⇒ device flow dark (PAT path only).
	s.githubClientID = strings.TrimSpace(os.Getenv("DEJIMAD_GITHUB_CLIENT_ID"))
	s.deviceSessions = newDeviceSessions()
	s.ghDeviceStartFn = githubDeviceStart
	s.ghDevicePollFn = githubDevicePoll
	return s
}

// SetWakeNotify toggles the default wake-on-message soft-notify (Lane 5 P3.5).
// The mailbox.arrival event still fires either way (the wrapper override path);
// this only gates Dejima's built-in nudge + wake-from-hibernate.
func (s *Server) SetWakeNotify(on bool) { s.wakeEnabled = on }

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
	// Carry the agent's human name on every event so subscribers (webhooks, the
	// TUI, the SDK) render a name, not a bare id. Resolved once here — the single
	// emit choke point — rather than at each of the ~10 emit call sites.
	if e.Agent != "" && e.AgentLabel == "" && e.Island != "" {
		e.AgentLabel = s.agentLabel(e.Island, e.Agent)
	}
	s.recordEvent(e)
	s.maybeUpdateAgentState(e)
	s.maybeUpdateAgentUsage(e)
	s.auditLifecycle(e) // append governance-relevant lifecycle events to the ledger (opt-in)
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

	// An agent reaching a turn boundary means it's running a real session — which
	// for claude-code means it's logged in. Piggyback that as a cheap "go check"
	// nudge to capture the operator's login host-side (no-op once seeded).
	s.tryAutoSeedClaude(e.Island)
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

// maybeUpdateAgentUsage ingests an agent.usage event (token counts an adapter
// reported over the in-island token path) into the per-(island,agent) usage
// snapshot. Cost is derived here from the model + token breakdown; Dejima can't
// see the LLM call so the counts must come from the agent itself.
func (s *Server) maybeUpdateAgentUsage(e events.Event) {
	if e.Island == "" || e.Type != events.TypeAgentUsage {
		return
	}
	u, ok := agentUsageFromPayload(e.Payload, e.Timestamp)
	if !ok {
		return
	}
	s.agentUsageMu.Lock()
	s.agentUsage[agentStateKey(e.Island, e.Agent)] = u
	s.agentUsageMu.Unlock()
}

// agentUsageFromPayload builds an AgentUsage from an agent.usage event payload.
// Expected keys (all numbers; absent → 0): input_tokens,
// cache_creation_input_tokens, cache_read_input_tokens, output_tokens; plus
// model + source strings. Returns ok=false when there are no tokens at all (a
// malformed/empty report shouldn't overwrite a real snapshot with zeros).
func agentUsageFromPayload(p map[string]any, ts time.Time) (AgentUsage, bool) {
	if p == nil {
		return AgentUsage{}, false
	}
	in := payloadInt(p, "input_tokens")
	cacheCreate := payloadInt(p, "cache_creation_input_tokens")
	cacheRead := payloadInt(p, "cache_read_input_tokens")
	out := payloadInt(p, "output_tokens")
	if in == 0 && cacheCreate == 0 && cacheRead == 0 && out == 0 {
		return AgentUsage{}, false
	}
	// Surface all input-side tokens (fresh + cached) as InputTokens so that
	// InputTokens + OutputTokens == TotalTokens for the UI; cost below still
	// prices the cache tiers separately for accuracy.
	inputAll := in + cacheCreate + cacheRead
	model := payloadString(p, "model")
	source := payloadString(p, "source")
	if source == "" {
		source = "agent"
	}
	u := AgentUsage{
		InputTokens:  inputAll,
		OutputTokens: out,
		TotalTokens:  inputAll + out,
		Source:       source,
		AsOf:         ts,
	}
	if cost, ok := usage.CostUSD(model, usage.Tokens{
		Input: in, CacheCreation: cacheCreate, CacheRead: cacheRead, Output: out,
	}); ok {
		u.CostUSD = &cost
	}
	return u, true
}

// agentUsageOf returns the latest usage snapshot for one agent, or nil.
func (s *Server) agentUsageOf(island, agentID string) *AgentUsage {
	s.agentUsageMu.Lock()
	defer s.agentUsageMu.Unlock()
	if u, ok := s.agentUsage[agentStateKey(island, agentID)]; ok {
		return &u
	}
	return nil
}

// payloadInt reads a non-negative integer from an event payload value. JSON
// decoding yields float64 (or json.Number); both are handled. Missing/invalid
// or negative → 0.
func payloadInt(p map[string]any, key string) int {
	switch v := p[key].(type) {
	case float64:
		if v < 0 {
			return 0
		}
		return int(v)
	case int:
		if v < 0 {
			return 0
		}
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil && n >= 0 {
			return int(n)
		}
	}
	return 0
}

// payloadString reads a string from an event payload value (missing → "").
func payloadString(p map[string]any, key string) string {
	if s, ok := p[key].(string); ok {
		return s
	}
	return ""
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
//
// roleAuth wraps the mux to apply the team-auth model (roleauth.go): a request
// with no bearer token runs as the trusted owner; one carrying a token is
// attenuated to that token's role + island scope. It also lands the resolved
// Identity (and Lane 1's AuditIdentity) on the request context for downstream
// handlers and the audit log.
//
// Composition: log → roleAuth (authenticate) → audit → mux (handle). roleAuth
// classifies on the mux but dispatches to auditMiddleware(mux), so the audit
// record is written with the authenticated identity already on the context
// (roleAuth stamps WithAuditIdentity). This is the authenticate→audit→handle
// order both Lane 1 and Lane 2 specified.
func (s *Server) Handler() http.Handler {
	mux := s.routes()
	return logMiddleware(s.log, s.roleAuth(mux, s.auditMiddleware(mux)))
}

// routes builds the route table shared by every listener. The differences
// between listeners live in the middleware that wraps this mux, never in the
// routes themselves, so there is exactly one source of truth for the API
// surface.
// routeRecorder is a ServeMux that remembers what it was asked to serve.
//
// The route-parity gate (sdk/openapi_parity.py) finds routes by matching
// literal `mux.HandleFunc("VERB /path", …)` strings in these sources. That is a
// defensible design and it has one blind spot: a route registered through a
// LOOP or a variable is invisible to it — undocumented, AND silently exempt
// from the gate whose entire job is catching undocumented routes.
//
// It was found the only way a textual scan can be: someone registered seven
// verbs in a loop, the gate reported one missing route, and they noticed the
// number was too small. That reflex works when you happen to know what the
// number should be, which is not a check.
//
// Recording what is ACTUALLY registered gives the test below something
// authoritative to compare the literal scan against, so the next person to loop
// is told rather than silently exempted.
// routeMux is what the Register* helpers take, so routes registered inside them
// are recorded too. *http.ServeMux satisfies it, so nothing else changes.
type routeMux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

type routeRecorder struct {
	mux      *http.ServeMux
	patterns []string
}

func (r *routeRecorder) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
	r.mux.HandleFunc(pattern, h)
}

// registeredRoutes returns every pattern routes() actually registered — the
// runtime truth, as opposed to what a regex over the source can see.
func (s *Server) registeredRoutes() []string {
	rec := &routeRecorder{mux: http.NewServeMux()}
	s.buildRoutes(rec)
	return rec.patterns
}

func (s *Server) routes() *http.ServeMux {
	rec := &routeRecorder{mux: http.NewServeMux()}
	s.buildRoutes(rec)
	return rec.mux
}

func (s *Server) buildRoutes(mux *routeRecorder) {
	mux.HandleFunc("GET /v1/islands", s.listIslands)
	mux.HandleFunc("POST /v1/islands", s.createIsland)
	mux.HandleFunc("GET /v1/islands/{name}", s.getIsland)
	mux.HandleFunc("GET /v1/islands/{name}/workspace-ready", s.workspaceReady)
	mux.HandleFunc("DELETE /v1/islands/{name}", s.deleteIsland)
	mux.HandleFunc("PATCH /v1/islands/{name}", s.updateIsland)
	mux.HandleFunc("POST /v1/islands/{name}/schedules", s.createSchedule)
	mux.HandleFunc("GET /v1/islands/{name}/schedules", s.listSchedules)
	mux.HandleFunc("DELETE /v1/islands/{name}/schedules/{id}", s.deleteSchedule)
	mux.HandleFunc("PUT /v1/islands/{name}/resources", s.updateIslandResources)
	mux.HandleFunc("POST /v1/islands/{name}/hibernate", s.hibernateIsland)
	mux.HandleFunc("POST /v1/islands/{name}/wake", s.wakeIsland)
	mux.HandleFunc("POST /v1/islands/{name}/reset", s.resetIsland)
	mux.HandleFunc("POST /v1/islands/{name}/upgrade", s.upgradeIsland)
	mux.HandleFunc("POST /v1/islands/{name}/clone", s.cloneIsland)
	mux.HandleFunc("POST /v1/image/build", s.handleImageBuild)
	// Managed local models (owner-only): orchestrate a host inference backend.
	mux.HandleFunc("GET /v1/local", s.handleLocalStatus)
	mux.HandleFunc("POST /v1/local/install", s.handleLocalInstall)
	mux.HandleFunc("GET /v1/local/models", s.handleLocalModels)
	mux.HandleFunc("POST /v1/local/models/{name}/pull", s.handleLocalPull)
	mux.HandleFunc("DELETE /v1/local/models/{name}", s.handleLocalRemove)
	mux.HandleFunc("POST /v1/local/off", s.handleLocalOff)
	mux.HandleFunc("POST /v1/admin/update", s.handleAdminUpdate)
	mux.HandleFunc("GET /v1/panic", s.handlePanicStatus)
	mux.HandleFunc("POST /v1/panic", s.handlePanic)
	mux.HandleFunc("DELETE /v1/panic", s.handleUnpanic)
	mux.HandleFunc("POST /v1/ssh/account-keys", s.handleAuthorizeAccountKey)
	mux.HandleFunc("GET /v1/ssh/account-keys", s.handleListAccountKeys)
	mux.HandleFunc("GET /v1/islands/{name}/session", s.sessionWS)
	mux.HandleFunc("GET /v1/islands/{name}/shell/session", s.islandShellWS)
	mux.HandleFunc("GET /v1/islands/{name}/agents", s.listAgents)
	mux.HandleFunc("POST /v1/islands/{name}/agents", s.addAgent)
	mux.HandleFunc("GET /v1/islands/{name}/agents/{id}", s.getAgent)
	mux.HandleFunc("DELETE /v1/islands/{name}/agents/{id}", s.removeAgent)
	mux.HandleFunc("PATCH /v1/islands/{name}/agents/{id}", s.updateAgent)
	mux.HandleFunc("POST /v1/islands/{name}/agents/{id}/move", s.moveAgent)
	mux.HandleFunc("POST /v1/islands/{name}/agents/{id}/restart", s.restartAgent)
	// The framework console, proxied. One handler for every verb: the daemon
	// relays bytes and does not model the gateway's API, so it has no business
	// deciding which methods that API accepts.
	//
	// Written out rather than looped, deliberately. sdk/openapi_parity.py finds
	// routes by matching a literal verb-and-path string in these sources, so a
	// loop over verbs registers seven routes the parity gate cannot see —
	// undocumented, and silently exempt from the check that exists to catch
	// exactly that. Repetition here buys visibility to the gate.
	//
	// (The example that would make this comment clearer cannot be written here:
	// the extractor reads comments too, so quoting the pattern invents a route.)
	mux.HandleFunc("GET /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("POST /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("PUT /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("DELETE /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("PATCH /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("HEAD /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("OPTIONS /v1/islands/{name}/agents/{id}/gateway/{path...}", s.handleAgentGateway)
	mux.HandleFunc("GET /v1/islands/{name}/agents/{id}/gateway-ready", s.getAgentGatewayReady)
	mux.HandleFunc("GET /v1/islands/{name}/agents/{id}/session", s.sessionWS)
	mux.HandleFunc("POST /v1/islands/{name}/mailbox", s.sendMailbox)
	mux.HandleFunc("GET /v1/islands/{name}/mailbox", s.pollMailbox)
	mux.HandleFunc("POST /v1/links", s.grantLink)
	mux.HandleFunc("GET /v1/links", s.listLinks)
	mux.HandleFunc("DELETE /v1/links", s.revokeLink)
	mux.HandleFunc("POST /v1/islands/{name}/link/send", s.sendLink)
	// Lane 5 Phase 3 — action delegation.
	mux.HandleFunc("GET /v1/islands/{name}/link/actions", s.listExposedActions)
	mux.HandleFunc("PUT /v1/islands/{name}/link/actions/{action}", s.exposeAction)
	mux.HandleFunc("DELETE /v1/islands/{name}/link/actions/{action}", s.unexposeAction)
	mux.HandleFunc("POST /v1/islands/{name}/link/action", s.requestAction)
	mux.HandleFunc("GET /v1/policy", s.listPolicy)
	mux.HandleFunc("POST /v1/policy", s.addPolicy)
	mux.HandleFunc("DELETE /v1/policy", s.removePolicy)
	mux.HandleFunc("GET /v1/link/actions", s.listPendingActions)
	mux.HandleFunc("GET /v1/link/actions/watch", s.watchActions)
	mux.HandleFunc("POST /v1/link/actions/{id}/approve", s.approveAction)
	mux.HandleFunc("POST /v1/link/actions/{id}/deny", s.denyAction)
	mux.HandleFunc("GET /v1/healthz", s.healthz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("PUT /v1/credentials/claude", s.handlePushClaudeCreds)
	mux.HandleFunc("GET /v1/credentials/claude", s.handleClaudeCredsStatus)
	mux.HandleFunc("GET /v1/credentials/github", s.handleGitHubIdentities)
	mux.HandleFunc("PUT /v1/credentials/github/{name}", s.handlePutGitHubIdentity)
	mux.HandleFunc("POST /v1/credentials/github/{name}/default", s.handleSetGitHubDefault)
	mux.HandleFunc("DELETE /v1/credentials/github/{name}", s.handleDeleteGitHubIdentity)
	mux.HandleFunc("GET /v1/credentials/github/{name}/repos", s.handleGitHubRepos)
	mux.HandleFunc("POST /v1/credentials/github/device-flow/start", s.handleGitHubDeviceStart)
	mux.HandleFunc("POST /v1/credentials/github/device-flow/poll", s.handleGitHubDevicePoll)
	mux.HandleFunc("GET /v1/credentials/providers", s.handleListProviderCreds)
	mux.HandleFunc("PUT /v1/credentials/providers/{provider}", s.handlePutProviderCred)
	mux.HandleFunc("DELETE /v1/credentials/providers/{provider}", s.handleDeleteProviderCred)
	mux.HandleFunc("GET /v1/agent-types", s.handleAgentTypes)
	mux.HandleFunc("GET /v1/agents/observed", s.handleObservedAgents)
	mux.HandleFunc("PATCH /v1/islands/{name}/agents/{id}/config", s.configureAgent)
	mux.HandleFunc("GET /v1/events/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /v1/events/subscribe", s.subscribeWebhook)
	mux.HandleFunc("DELETE /v1/events/subscriptions/{id}", s.unsubscribeWebhook)
	mux.HandleFunc("POST /v1/internal/agent-event", s.handleAgentEvent)
	mux.HandleFunc("POST /v1/sessions/revoke", s.handleRevokeSessions)
	mux.HandleFunc("GET /v1/clients", s.handleClientHistory)
	mux.HandleFunc("GET /v1/overview", s.handleOverview)
	mux.HandleFunc("GET /v1/aggregate", s.handleAggregate)
	// Host terminals (operator-only, gated; never in tokenauth's allow-list).
	mux.HandleFunc("GET /v1/terminals", s.handleListTerminals)
	mux.HandleFunc("POST /v1/terminals", s.handleCreateTerminal)
	mux.HandleFunc("DELETE /v1/terminals/{id}", s.handleDeleteTerminal)
	mux.HandleFunc("PATCH /v1/terminals/{id}", s.handleRelabelTerminal)
	mux.HandleFunc("GET /v1/terminals/{id}/session", s.terminalSessionWS)
	mux.HandleFunc("GET /v1/islands/{name}/events", s.handleIslandEvents)
	mux.HandleFunc("GET /v1/islands/{name}/secrets", s.handleListSecrets)
	mux.HandleFunc("PUT /v1/islands/{name}/secrets/{key}", s.handlePutSecret)
	mux.HandleFunc("DELETE /v1/islands/{name}/secrets/{key}", s.handleDeleteSecret)
	mux.HandleFunc("GET /v1/islands/{name}/egress", s.handleIslandEgress)
	mux.HandleFunc("GET /v1/islands/{name}/egress/policy", s.handleGetEgressPolicy)
	mux.HandleFunc("PATCH /v1/islands/{name}/egress/policy", s.handlePatchEgressPolicy)
	mux.HandleFunc("GET /v1/islands/{name}/spawn-grant", s.getSpawnGrant)
	mux.HandleFunc("POST /v1/islands/{name}/spawn-grant", s.setSpawnGrant)
	mux.HandleFunc("DELETE /v1/islands/{name}/spawn-grant", s.revokeSpawnGrant)
	mux.HandleFunc("POST /v1/islands/{name}/exec", s.handleExec)
	mux.HandleFunc("GET /v1/islands/{name}/files/{path...}", s.handleReadFile)
	mux.HandleFunc("PUT /v1/islands/{name}/files/{path...}", s.handleWriteFile)
	mux.HandleFunc("GET /v1/islands/{name}/logs", s.handleLogs)
	mux.HandleFunc("GET /v1/islands/{name}/port/scopes", s.handleListPortScopes)
	mux.HandleFunc("POST /v1/islands/{name}/port/scopes", s.handleGrantPortScope)
	mux.HandleFunc("DELETE /v1/islands/{name}/port/scopes/{scope}", s.handleRevokePortScope)
	mux.HandleFunc("GET /v1/islands/{name}/github/host-credential", s.handleGetHostGitHubCredential)
	mux.HandleFunc("POST /v1/islands/{name}/github/host-credential", s.handleGrantHostGitHubCredential)
	mux.HandleFunc("DELETE /v1/islands/{name}/github/host-credential", s.handleRevokeHostGitHubCredential)
	// Capability broker — grant surface (operator-only; absent from
	// tokenRouteAccess, so a contained brain can never self-grant). Execution
	// lands in a later phase. See internal/api/capability.go.
	mux.HandleFunc("GET /v1/islands/{name}/capability/grants", s.handleListCapabilityGrants)
	mux.HandleFunc("POST /v1/islands/{name}/capability/grants", s.handleGrantCapability)
	mux.HandleFunc("DELETE /v1/islands/{name}/capability/grants/{target}", s.handleRevokeCapability)
	// Capability execution — the in-island brain invokes a granted target. Unlike
	// the grant routes (operator-only), this one IS token-reachable (accessTokenOwn
	// in tokenauth.go): pinned to the token's own island, authorized by its grant.
	mux.HandleFunc("POST /v1/capabilities/execute", s.handleCapabilityExecute)
	mux.HandleFunc("POST /v1/islands/{name}/port/intake", s.handlePortIntake)
	mux.HandleFunc("POST /v1/islands/{name}/port/export", s.handlePortExport)
	mux.HandleFunc("POST /v1/islands/{name}/port/write", s.handlePortWrite)
	s.RegisterAudit(mux) // GET /v1/audit (read · filter · export · verify)
	// MCP broker — deny-all grants of named host MCP servers + the brokered,
	// ledgered call path. Grant routes are operator-only (absent from
	// tokenRouteAccess). See internal/api/mcp.go + docs/mcp-broker-spec.md.
	s.RegisterMCP(mux)
	// Unified per-island grants view — every grant type (Port, capability, MCP,
	// links) in one operator-readable call. Aggregates the four list endpoints
	// above; owns no store. See internal/api/grants.go.
	mux.HandleFunc("GET /v1/islands/{name}/grants", s.handleListGrants)
	// Team-auth: token issuance/list/revoke (owner-only; see roleauth.go +
	// tokens.go). Registered as one append-only line per the lane seam contract.
	s.RegisterAuth(mux)
	// Team activity feed — the curated, owner-enriched view over the audit ledger
	// (viewer-readable; see activity.go). One append-only line per the seam contract.
	s.RegisterActivity(mux)
	// Per-island visual identity — operator-only color+glyph override (absent from
	// tokenRouteAccess, so a contained island can never set its own identity). The
	// override is reflected back in IslandInfo.Identity. See internal/api/identity.go.
	mux.HandleFunc("PUT /v1/islands/{name}/identity", s.setIslandIdentity)
	mux.HandleFunc("PUT /v1/islands/{name}/github-identity", s.handleSetIslandGitHubIdentity)
	mux.HandleFunc("POST /v1/islands/{name}/agents/{id}/update", s.handleUpdateAgent)
	mux.HandleFunc("DELETE /v1/islands/{name}/identity", s.clearIslandIdentity)
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
			s.reconcileAgentsAsync(p, false)
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
func (s *Server) handleGitHubIdentities(w http.ResponseWriter, r *http.Request) {
	store, err := githubid.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// An operator sees only their own tenant's identities (plus host-shared ones);
	// the host owner sees all.
	owner, ownsAll := s.callerGHScope(r.Context())
	views, dangling := identityViews(store, store.ListForOwner(owner, ownsAll))
	writeJSON(w, http.StatusOK, GitHubIdentitiesResponse{Identities: views, Dangling: dangling})
}

// handlePutGitHubIdentity adds or updates a named GitHub identity. This is how
// a credentialed client seeds the daemon (`dejima auth push --github`).
func (s *Server) handlePutGitHubIdentity(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := githubid.ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	// Tenancy: the identity is stamped with the AUTHENTICATED caller's owner
	// (server-authoritative — an operator can only push into their OWN tenant).
	// Only the host owner may set the daemon default or mark an identity shared.
	owner, ownsAll := s.callerGHScope(r.Context())
	if !ownsAll && (req.Default || req.Shared) {
		writeError(w, http.StatusForbidden, errors.New("only the host owner can set a github identity as default or shared"))
		return
	}
	store, err := githubid.Update(func(st *githubid.Store) error {
		st.PutOwned(githubid.Identity{
			Name: name, Login: req.Login, ID: req.ID, Host: req.Host, Token: req.Token,
			Owner: owner, Shared: ownsAll && req.Shared, Scopes: req.Scopes,
		})
		if req.Default {
			_ = st.SetDefaultFor(owner, name)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("github identity stored", "name", name, "login", req.Login, "owner", owner, "shared", ownsAll && req.Shared)
	// The store changed, so every island's materialized gh credential is now
	// potentially stale. Rewrite them: the mount is a directory, so a running
	// container sees it without a recreate.
	s.refreshIslandGitHubConfigs()
	views, dangling := identityViews(store, store.ListForOwner(owner, ownsAll))
	writeJSON(w, http.StatusOK, GitHubIdentitiesResponse{Identities: views, Dangling: dangling})
}

// handleSetGitHubDefault points the caller's default at an EXISTING identity.
//
// A separate route rather than a flag on PUT, because PUT requires a login and
// token: choosing among identities you already have would otherwise mean
// re-supplying a credential you are not changing, and relaxing PUT's validation
// to allow a token-less update would make "seed an identity" and "pick one"
// the same call with different meanings depending on which fields are blank.
//
// This gap is why an operator spent an hour on a 401. `dejima github connect`
// without --default creates a SECOND identity, the resolver picks the DEFAULT
// rather than the newest, and there was no route to say which one to use. You
// could add identities and not manage them.
func (s *Server) handleSetGitHubDefault(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	owner, ownsAll := s.callerGHScope(r.Context())
	var known bool
	store, err := githubid.Update(func(st *githubid.Store) error {
		for _, m := range st.ListForOwner(owner, ownsAll) {
			if m.Name == name {
				known = true
			}
		}
		if !known {
			return nil // reported as 404 below; never create by side effect
		}
		return st.SetDefaultFor(owner, name)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !known {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such github identity %q", name))
		return
	}
	s.log.Info("github default identity set", "name", name, "owner", owner)
	// The store changed, so every island's materialized gh credential is now
	// potentially stale. Rewrite them: the mount is a directory, so a running
	// container sees it without a recreate.
	s.refreshIslandGitHubConfigs()
	views, dangling := identityViews(store, store.ListForOwner(owner, ownsAll))
	writeJSON(w, http.StatusOK, GitHubIdentitiesResponse{Identities: views, Dangling: dangling})
}

// handleDeleteGitHubIdentity removes a GitHub identity. An operator can delete
// only their OWN tenant's identity; the host owner deletes host identities.
func (s *Server) handleDeleteGitHubIdentity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	owner, _ := s.callerGHScope(r.Context())
	var missing bool
	if _, err := githubid.Update(func(st *githubid.Store) error {
		missing = !st.DeleteOwned(owner, name)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if missing {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such github identity %q", name))
		return
	}
	affected := s.islandsUsingIdentity(owner, name)
	if len(affected) > 0 {
		s.log.Warn("deleted a github identity still referenced by islands",
			"name", name, "islands", affected)
	}
	// The store changed, so every island's materialized gh credential is now
	// potentially stale. Rewrite them: the mount is a directory, so a running
	// container sees it without a recreate.
	s.refreshIslandGitHubConfigs()
	writeJSON(w, http.StatusOK, DeleteGitHubIdentityResponse{AffectedIslands: affected})
}

// islandsUsingIdentity returns the names of the owner's islands that reference
// the named GitHub identity. Best-effort: a project-list failure yields no names
// rather than blocking the delete.
func (s *Server) islandsUsingIdentity(owner, name string) []string {
	projects, err := project.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range projects {
		if p.GitHubIdentity == name && ghOwner(p.Owner) == owner {
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
	owner, _ := s.callerGHScope(r.Context())
	id, ok := store.ResolveForIsland(owner, r.PathValue("name"))
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
		if !s.visibleTo(r.Context(), p) {
			continue // private visibility (P2): a teammate sees only its own islands
		}
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
	// Bound the in-container inspection: a busy (first-boot npm install) or wedged
	// container can make `docker exec`/`inspect` slow or hang, and detail is polled
	// continuously by the TUI — an unbounded poll would make the UI look frozen.
	// Cap it; on timeout the slow fields just come back empty/degraded.
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	info := s.toInfo(ctx, p)
	// Per-agent session liveness is detail-only (one exec per agent).
	info.Agents = s.agentInfos(ctx, p, info.Container == string(runtime.StatusRunning))
	// (Resource caps are populated in toInfo() — present on both list and detail.)
	// Git status is only computed in the detail view, not the list, because
	// it requires container exec and is the slowest field to populate.
	info.Git = s.gitStatusOf(ctx, p.ContainerName())
	// Whether this container reaps orphans. Detail-only: one more inspect, and
	// the answer cannot change without a recreate.
	//
	// Asked of the RUNTIME rather than read off our own source. The daemon passes
	// --init today, so the code says every island reaps; a container created
	// before that flag existed says otherwise and will go on leaking a zombie per
	// orphaned process until it is recreated. Left nil on error, because "I
	// couldn't ask" must not render as "fine" — see IslandInfo.ReapsOrphans.
	if reaps, err := s.rt.ContainerReapsOrphans(ctx, p.ContainerName()); err == nil {
		info.ReapsOrphans = &reaps
	}
	// Crash health is one extra inspect; detail-only to keep list refreshes cheap.
	if h, err := s.rt.Inspect(ctx, p.ContainerName()); err == nil {
		info.Health = &IslandHealth{
			OOMKilled:    h.OOMKilled,
			RestartCount: h.RestartCount,
			ExitCode:     h.ExitCode,
		}
	}
	// Per-island disk usage (workspace + home volumes). Detail-only and cached
	// because `docker system df -v` is slow; 0 means the driver didn't report it.
	if sizes := s.volumeSizes(ctx); sizes != nil {
		ws, home := sizes[p.WorkspaceVolume()], sizes[p.HomeVolume()]
		if ws > 0 || home > 0 {
			info.Disk = &IslandDisk{WorkspaceBytes: ws, HomeBytes: home, TotalBytes: ws + home}
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// workspaceReady reports whether the island's repo clone has landed in
// /workspace (i.e. /workspace/.git exists). `dejima connect` polls this so it
// doesn't drop the operator into an empty /workspace while the entrypoint is
// still cloning. One cheap exec; safe to call repeatedly.
func (s *Server) workspaceReady(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// A repo-less island has no /workspace/.git and never will, so the probe
	// below can only ever fail. Left to run, the caller polls for its full
	// two-minute budget and then reports "stalled" — a working island presented
	// as a broken one, after the slowest possible wait. There is nothing to
	// clone, so it is ready the moment it exists.
	if p.NoRepo {
		writeJSON(w, http.StatusOK, WorkspaceReadyResponse{Ready: true})
		return
	}
	// Bounded: this is polled in a loop and a busy container shouldn't stall it.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ready := false
	if _, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"test", "-e", "/workspace/.git"}); err == nil && code == 0 {
		ready = true
	}
	resp := WorkspaceReadyResponse{Ready: ready}
	if !ready {
		// Not ready yet: is it still cloning, or did the clone FAIL? The entrypoint
		// records a classified reason at this path on failure (report_clone_failure
		// in image/start.sh) — its presence means a failed clone, so connect can
		// surface it and stop instead of waiting the full window then attaching to a
		// repo-less /workspace. One extra cheap cat, only on the not-ready branch.
		if out, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"cat", "/home/dejima/.dejima/clone-status"}); err == nil && code == 0 {
			if reason := strings.TrimSpace(out); reason != "" {
				resp.CloneFailed = true
				resp.CloneReason = reason
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
	infos := s.agentInfos(r.Context(), p, s.agentsLive(r.Context(), p))
	// An island token reaching its OWN roster (allowlisted accessOwnIsland) gets a
	// reduced peer view — the discovery/addressing directory only. Operator and
	// team tokens (TokenIslandFromContext == "") keep the full AgentInfo.
	if TokenIslandFromContext(r.Context()) != "" {
		infos = islandPeerRoster(infos)
	}
	writeJSON(w, http.StatusOK, infos)
}

// islandPeerRoster projects AgentInfos to the reduced view an island token may
// see of its co-resident peers. It keeps id/label/type/state/branch/worktree —
// data a peer can already read directly off the shared /workspace (peer worktrees
// live at /workspace/.agents/<id>) — and drops everything that is config,
// credential, or attach-surface (provider, model, key status, tmux, attachable,
// restarts, error, presence, agent-state, created-at). The contained agent thus
// learns nothing it couldn't already see, just ergonomically. See
// docs/intra-island-coordination-spec.md (P1).
func islandPeerRoster(infos []AgentInfo) []AgentInfo {
	out := make([]AgentInfo, len(infos))
	for i, ai := range infos {
		out[i] = AgentInfo{
			// Carried, not re-asserted: the roster is a projection of an island's
			// own agent list, so the level was already decided at that boundary.
			// Dropping it would make every peer read as unset, which callers must
			// treat as not-contained — safe, but wrong, and the wrong kind of wrong
			// for a list whose whole purpose is "who else is in here with me".
			Containment: ai.Containment,
			ID:          ai.ID,
			Label:       ai.Label,
			Type:        ai.Type,
			State:       ai.State,
			Branch:      ai.Branch,
			Worktree:    ai.Worktree,
		}
	}
	return out
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
		Cmd:       cmd,
		Tmux:      "agent-" + id,
		Branch:    "agent/" + id,
		Worktree:  agentsWorktreeRoot + "/" + id,
		Provider:  strings.TrimSpace(req.Provider),
		Model:     normalizeModel(req.Provider, req.Model),
		Ephemeral: req.Ephemeral,
		SpawnedBy: strings.TrimSpace(req.SpawnedBy),
		CreatedAt: time.Now().UTC(),
	}
	// Assign the agent's label. Root cause of "Dejima shows raw ids everywhere":
	// agents used to default to a blank label, so every surface fell back to the
	// bare id. Now an agent always gets a meaningful, unique, non-blank label:
	//   - no label requested → derive a readable default from the Type
	//     ("claude-code" → "claude", unknown → "agent"), deduped to "claude-2", …;
	//   - a label requested → keep it, deduped against existing agents ("build" →
	//     "build-2" if taken). Empty labels never reach the spec.
	// The returned AgentInfo carries the final label so the CLI/TUI can surface it.
	if strings.TrimSpace(req.Label) == "" {
		spec.Label = p.DefaultAgentLabel(spec, "")
	} else {
		spec.Label = p.UniqueAgentLabel(req.Label, "")
	}
	// A plain terminal pokes at the island's workspace directly — no isolated
	// worktree/branch, just a shell on /workspace.
	if typ == handlers.Shell {
		spec.Branch = ""
		spec.Worktree = "/workspace"
	}
	// Co-located headless agents self-restart by default so a crash doesn't end
	// the agent silently — EXCEPT ephemeral sub-agents, which are run-once: they
	// must exit when done so the reaper can free their budget slot (a restarting
	// ephemeral agent would never exit).
	if !handlers.Attachable(typ) && !spec.Ephemeral {
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
	// Spawn gate: an in-island token caller is an agent-INITIATED spawn — governed
	// by the operator's spawn grant (deny by default). Enforced here, under the
	// per-island projectLock held above, so the budget check is ATOMIC with the
	// create (no TOCTOU on a concurrent spawn burst). Operator callers
	// (TokenIslandFromContext == "") are unaffected.
	isSpawn := TokenIslandFromContext(r.Context()) != ""
	if isSpawn {
		if err := s.authorizeSpawn(p, spec); err != nil {
			s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
				Type: "spawn.deny", Island: name, Scope: spec.Type,
				Detail: err.Error(), Actor: "agent:" + spec.SpawnedBy, Decision: "denied",
			})
			writeError(w, http.StatusForbidden, err)
			return
		}
	}
	id := spec.ID
	typ := spec.Type
	p.AddAgent(spec)
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if isSpawn {
		// Reserve a lifetime-budget slot (max_total) atomically — still under the
		// projectLock — and ledger the spawn with its lineage.
		_, _ = spawn.Update(func(st *spawn.Store) error { st.Consume(name); return nil })
		s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
			Type: "spawn.create", Island: name, Scope: spec.Type,
			Detail: "sub-agent " + id + " spawned by " + spec.SpawnedBy, Actor: "agent:" + spec.SpawnedBy, Decision: "allowed",
		})
	}
	if s.agentsLive(r.Context(), p) {
		if err := s.ensureAgentSession(r.Context(), p, &p.Agents[len(p.Agents)-1], false); err != nil {
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
		Payload: map[string]any{"type": typ, "label": spec.Label},
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
	force := r.URL.Query().Get("force") == "true"
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
	// Agent self-reap (#64): an in-island token caller may DELETE an agent ONLY
	// when it's an EPHEMERAL spawned sub-agent — so a contained agent can tear
	// down the sub-agents it spawned (explicit teardown alongside the auto-reaper)
	// without the operator becoming the GC. accessOwnIsland already pins {name} to
	// the token's island; this adds the ephemeral+lineage constraint. The primary,
	// a non-ephemeral peer, and any other island stay denied for a token caller.
	// Within one island all agents share the token = one trust domain (no
	// per-agent boundary), so any agent in the island may reap that island's
	// ephemeral sub-agents; cross-island is structurally impossible. Operator
	// callers (no token island) are unaffected.
	isTokenReap := TokenIslandFromContext(r.Context()) != ""
	if isTokenReap && (!a.Ephemeral || a.SpawnedBy == "") {
		writeError(w, http.StatusForbidden, errors.New(
			"an island token may only remove ephemeral sub-agents (this agent is not an ephemeral spawned sub-agent)"))
		return
	}
	// Any agent can be removed — an island with no agents is valid (you shell into
	// it, or add agents later); the container's tail -f keepalive outlives them.
	// The one exception: a headless FIRST agent IS the container's PID 1
	// (image/start.sh runs it as the main process), so removing it would stop the
	// island. Direct the user to hibernate/purge instead. (Path B removes this
	// coupling; see docs/island-pid1-unification.md.)
	if len(p.Agents) > 0 && p.Agents[0].ID == id && !handlers.Attachable(p.Agents[0].Type) {
		writeError(w, http.StatusConflict, errors.New(
			"this agent is the island's main process (PID 1) — hibernate or purge the island instead"))
		return
	}
	// Guard the agent's worktree, unless forced. Deliberately AFTER the PID-1 and
	// self-reap checks (those are about whether the removal is allowed at all)
	// and BEFORE anything is persisted — a guard that fires after the config is
	// saved would leave the agent half-removed.
	//
	// An island token's self-reap is exempt: an ephemeral sub-agent tearing itself
	// down is the designed teardown path, its worktree is scratch by construction,
	// and there is no operator present to read a message or pass a flag. Blocking
	// it would strand sub-agents and make the operator the GC — the exact thing
	// self-reap exists to avoid.
	if !force && !isTokenReap {
		if riskErr := s.agentRemovalRiskError(r.Context(), p, a); riskErr != nil {
			writeError(w, http.StatusConflict, riskErr)
			return
		}
	}
	// Persist the removal first (the source of truth), then clean up the agent's
	// tmux session + worktree best-effort. The cleanup execs into the container,
	// which can be busy or wedged — so it runs detached and bounded rather than
	// inline: a hung container must never block the removal (which would freeze a
	// client holding this island's lock). The container ceiling (runtime) is the
	// backstop; this just keeps the request snappy.
	live := s.agentsLive(r.Context(), p)
	agentCopy := *a
	p.RemoveAgent(id)
	s.clearAgentError(name, id)
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if live {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.removeAgentSession(ctx, p, &agentCopy)
		}()
	}
	if isTokenReap {
		// Ledger the agent-initiated teardown (a privileged crossing), matching the
		// auto-reaper's spawn.reap shape so audit sees both paths uniformly.
		s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
			Type: "spawn.reap", Island: name,
			Detail:   "self-reaped sub-agent " + id + " (spawned by " + agentCopy.SpawnedBy + ")",
			Decision: "allowed",
		})
	}
	s.emit(events.Event{
		Type:    events.TypeIslandAgentRemoved,
		Island:  name,
		Agent:   id,
		Payload: map[string]any{"label": agentCopy.Label},
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
	// Apply only the fields the request actually sent (pointers) so updating one
	// can't clobber the other.
	if req.Title != nil {
		p.Title = strings.TrimSpace(*req.Title) // empty clears it
	}
	if req.NoHibernate != nil {
		p.NoHibernate = *req.NoHibernate
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// updateIslandResources changes an island's memory limit and/or OOM priority
// (the per-island stack-rank knob). Memory applies live via `docker update`;
// OOM priority is set at container create, so a change is persisted and flagged
// restart_required (it takes effect on the next recreate/upgrade). Operator-only:
// absent from tokenRouteAccess, so an in-island token is default-denied.
func (s *Server) updateIslandResources(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req UpdateResourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	memChanged, prioChanged := false, false
	if req.Memory != nil && *req.Memory != p.Resources.Memory {
		p.Resources.Memory = strings.TrimSpace(*req.Memory) // "" → unlimited
		memChanged = true
	}
	if req.OOMPriority != nil && (p.Resources.OOMPriority == nil || *req.OOMPriority != *p.Resources.OOMPriority) {
		v := *req.OOMPriority
		p.Resources.OOMPriority = &v
		prioChanged = true
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Apply the memory limit live (no-op if the container isn't running or memory
	// didn't change). OOM priority can't be changed live — it needs a recreate.
	if memChanged {
		if err := s.rt.UpdateResources(r.Context(), p.ContainerName(), p.Resources.Memory); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("apply memory limit: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, UpdateResourcesResponse{
		Resources: Resources{
			Memory:      p.Resources.Memory,
			CPUs:        p.Resources.CPUs,
			Disk:        p.Resources.Disk,
			OOMPriority: p.Resources.OOMPriority,
		},
		RestartRequired: prioChanged,
	})
}

// updateAgent changes an agent's cosmetic label. Everything else (id, type,
// worktree, session) is immutable — the id is the stable handle, the label is
// the renamable display name, mirroring the island Name / agent Label split.
// MoveAgentRequest reorders an agent within its island's list. Delta is the
// number of positions to shift (negative = toward the front); it's clamped to
// the ends.
type MoveAgentRequest struct {
	Delta int `json:"delta"`
}

// moveAgent reorders an agent within its island. Order is cosmetic (the
// dashboard/CLI no longer key off position), except Agents[0] still seeds the
// container entrypoint on the next recreate — moving a headless first agent off
// slot 0 only matters then; see docs/island-pid1-unification.md.
func (s *Server) moveAgent(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, ok := p.AgentByID(id); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, id))
		return
	}
	var req MoveAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if p.MoveAgent(id, req.Delta) {
		if err := p.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.agentInfos(r.Context(), p, false))
}

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
	// Dedupe against other agents (excluding this one, so renaming to its own
	// current label is a no-op, not "-2"). Empty clears the label and passes
	// through unchanged. The returned AgentInfo carries the assigned label, so a
	// collision-driven "build" → "build-2" is discoverable by request/response diff.
	a.Label = p.UniqueAgentLabel(req.Label, id)
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
	// no_repo is the deliberate empty-workspace mode. Kept as its own opt-in
	// rather than letting an empty repo through: an empty Repo is far more often
	// a shell-eaten URL than an intent, and silently booting an empty island for
	// it is indistinguishable from a clone that failed.
	fromDir := strings.TrimSpace(req.FromDir)
	switch {
	case fromDir != "" && (strings.TrimSpace(req.Repo) != "" || strings.TrimSpace(req.SeedPath) != "" || req.NoRepo):
		writeError(w, http.StatusBadRequest, errors.New("from_dir is its own workspace source — it can't be combined with a repo, a seed, or no_repo"))
		return
	case fromDir != "" && strings.TrimSpace(req.Name) == "":
		// Deriving one from the directory's basename is tempting and wrong: the
		// same folder name recurs everywhere ("src", "notes", "tmp"), so the
		// island would be named something unpredictable and often colliding.
		writeError(w, http.StatusBadRequest, errors.New("name is required with from_dir (a folder name is not a good island name)"))
		return
	case req.GitInit && fromDir == "":
		writeError(w, http.StatusBadRequest, errors.New("git_init only applies with from_dir"))
		return
	case req.KeepScope && fromDir == "":
		writeError(w, http.StatusBadRequest, errors.New("keep_scope only applies with from_dir"))
		return
	case req.NoRepo && (strings.TrimSpace(req.Repo) != "" || strings.TrimSpace(req.SeedPath) != ""):
		writeError(w, http.StatusBadRequest, errors.New("no_repo can't be combined with a repo or seed — pick one"))
		return
	case req.NoRepo && strings.TrimSpace(req.Name) == "":
		writeError(w, http.StatusBadRequest, errors.New("name is required with no_repo (there's no repo to derive one from)"))
		return
	case !req.NoRepo && fromDir == "" && strings.TrimSpace(req.Repo) == "" && strings.TrimSpace(req.SeedPath) == "":
		writeError(w, http.StatusBadRequest, errors.New("repo is required (a URL, a local path, or a seed) — or from_dir to seed from a folder, or no_repo for an empty workspace"))
		return
	}
	// Stop a doomed private-repo clone before it launches into an empty, repo-less
	// island (see create_identity_gate.go). A seed-backed create clones locally, so
	// only gate when there's no seed source.
	if strings.TrimSpace(req.SeedPath) == "" {
		if err := s.blockDoomedClone(r.Context(), req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
		// Names are globally unique (they key the container/volume/network). To a
		// non-owner, a taken name must not reveal WHOSE island it is (or even that
		// it's someone else's) — return a generic "unavailable". The host owner,
		// who can see the fleet anyway, gets the actionable message.
		if id, ok := IdentityFromContext(r.Context()); ok && !id.OwnsAll() {
			writeError(w, http.StatusConflict, fmt.Errorf("island name %q is unavailable; choose another (or pass --name)", name))
		} else {
			writeError(w, http.StatusConflict, fmt.Errorf("island %q already exists; use --name to disambiguate", name))
		}
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
	// A home island runs an always-on assistant brain, so it must be a headless
	// (non-attachable) agent — either the reserved "headless" type with a cmd, or
	// a baked-launch headless handler like "openclaw". Gate on attachability, not
	// the literal "headless" type, so first-class headless agents qualify; an
	// interactive agent (claude-code, codex) is rejected.
	if req.Role == project.RoleHome && handlers.Attachable(agent) {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("a home island runs an assistant brain — it must be a headless agent (e.g. %q, or %q with a cmd)", "openclaw", AgentHeadless))
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
		// The new island will be owned by the caller, so validate the identity
		// resolves within the caller's tenant (own + host-shared).
		callerOwner, _ := s.callerGHScope(r.Context())
		if _, ok := store.ResolveForIsland(callerOwner, gid); !ok {
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

	// from_dir seeds a repo-less island: no clone, then a brokered copy.
	p, err := s.provision(r.Context(), name, req.Repo, agent, image, cmd, req.Role, req.GitHubIdentity, req.Resources, req.SeedPath, req.Agents, req.NoRepo || fromDir != "")
	if err != nil {
		// Best-effort cleanup: remove anything we created if provisioning failed mid-flight.
		s.log.Error("provision failed; cleaning up", "name", name, "err", err)
		_ = s.teardown(context.Background(), p, true)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Stamp ownership from the AUTHENTICATED caller (server-authoritative — never
	// req.Owner, which a teammate could forge). The island belongs to the caller's
	// tenant; the host owner (local socket / RoleOwner) attributes to HostOwner.
	// This is what makes create work for an owner-scoped teammate AND keeps the new
	// island private to them.
	owner := project.HostOwner()
	if id, ok := IdentityFromContext(r.Context()); ok && strings.TrimSpace(id.Owner) != "" {
		owner = strings.TrimSpace(id.Owner)
	}
	p.Owner = owner
	if len(req.Tags) > 0 {
		p.Tags = sanitizeTags(req.Tags)
	}
	if err := p.Save(); err != nil {
		s.log.Warn("save ownership metadata", "island", p.Name, "err", err)
	}

	// Seed from a host folder AFTER provisioning succeeded, deliberately outside
	// the block above: a failure here must not reach that teardown. The island
	// itself is fine by this point, so destroying it would make the operator
	// recreate a working island to retry a copy they can re-run with
	// `port intake -r`.
	if fromDir != "" {
		if err := s.seedWorkspaceFromDir(r.Context(), p, folderSeed{
			Dir: fromDir, KeepScope: req.KeepScope, GitInit: req.GitInit,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf(
				"island %q was created, but seeding /workspace from %q failed: %w — "+
					"the source folder is untouched; retry with `dejima port grant %s %s:ro` "+
					"then `dejima port intake %s <scope>:. -r`",
				p.Name, fromDir, err, p.Name, fromDir, p.Name))
			return
		}
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

// cloneProjectConfig builds a new Project that duplicates src under newName.
// Title and Ports are deliberately dropped: Title is cosmetic (the clone shows
// its own name), and Ports are host-filesystem grants — a clone starts deny-all
// and never silently inherits the source's host access. The home + workspace
// volumes (copied separately) carry tool credentials and the git history along.
func cloneProjectConfig(src *project.Project, newName string, now time.Time) *project.Project {
	dst := &project.Project{
		Name:           newName,
		RepoURL:        src.RepoURL,
		Agent:          src.Agent,
		Image:          src.Image,
		Cmd:            src.Cmd,
		Resources:      src.Resources,
		Role:           src.Role,
		GitHubIdentity: src.GitHubIdentity,
		Owner:          src.Owner,
		DesiredState:   project.StateRunning,
		CreatedAt:      now,
		LastUsedAt:     now,
		// Grants are deliberately NOT cloned (Ports/Capabilities aren't either): a
		// copy starts deny-all and the operator re-grants what it needs. Marking it
		// reviewed keeps the migration from quietly re-granting what the clone was
		// just denied.
		HostGitHubReviewed: true,
	}
	if len(src.Agents) > 0 {
		dst.Agents = make([]project.AgentSpec, len(src.Agents))
		copy(dst.Agents, src.Agents)
	}
	if len(src.Tags) > 0 {
		dst.Tags = make(map[string]string, len(src.Tags))
		for k, v := range src.Tags {
			dst.Tags[k] = v
		}
	}
	return dst
}

// cloneIsland duplicates an island: a new config plus byte-for-byte copies of
// its workspace and home volumes (so credentials, tool auth, and git history
// come along). Caveats are inherent: device/host-bound tokens may not survive,
// and duplicating the home volume duplicates session/runtime state.
func (s *Server) cloneIsland(w http.ResponseWriter, r *http.Request) {
	srcName := r.PathValue("name")
	var req CloneIslandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	newName := strings.TrimSpace(req.NewName)
	if err := project.ValidateName(newName); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if newName == srcName {
		writeError(w, http.StatusBadRequest, errors.New("clone target must differ from the source"))
		return
	}

	// Lock the new name (the resource being created), mirroring createIsland.
	lock := s.projectLock(newName)
	lock.Lock()
	defer lock.Unlock()

	src, err := project.Load(srcName)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if project.Exists(newName) {
		writeError(w, http.StatusConflict, fmt.Errorf("island %q already exists", newName))
		return
	}

	dst := cloneProjectConfig(src, newName, time.Now().UTC())
	if err := project.EnsureProjectSubdirs(newName); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := dst.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Provision volumes/network, copy the source data in, then bring up the
	// container. Volumes must be populated BEFORE the container starts so the
	// entrypoint sees /workspace/.git and skips re-cloning the repo.
	fail := func(err error) {
		s.log.Error("clone failed; cleaning up", "src", srcName, "new", newName, "err", err)
		_ = s.teardown(context.Background(), dst, true)
		writeError(w, http.StatusInternalServerError, err)
	}
	if err := s.rt.EnsureVolume(r.Context(), dst.WorkspaceVolume()); err != nil {
		fail(fmt.Errorf("create workspace volume: %w", err))
		return
	}
	if err := s.rt.EnsureVolume(r.Context(), dst.HomeVolume()); err != nil {
		fail(fmt.Errorf("create home volume: %w", err))
		return
	}
	if err := s.rt.EnsureNetwork(r.Context(), dst.NetworkName()); err != nil {
		fail(fmt.Errorf("create network: %w", err))
		return
	}
	if err := s.rt.CopyVolumeData(r.Context(), src.WorkspaceVolume(), dst.WorkspaceVolume(), src.Image); err != nil {
		fail(fmt.Errorf("copy workspace volume: %w", err))
		return
	}
	if err := s.rt.CopyVolumeData(r.Context(), src.HomeVolume(), dst.HomeVolume(), src.Image); err != nil {
		fail(fmt.Errorf("copy home volume: %w", err))
		return
	}
	if err := s.createContainerForProject(r.Context(), dst, "", false); err != nil {
		fail(err)
		return
	}
	s.reconcileAgentsAsync(dst, false)
	s.emit(events.Event{Type: events.TypeIslandCreated, Island: dst.Name})
	s.emit(events.Event{Type: events.TypeIslandRunning, Island: dst.Name})
	writeJSON(w, http.StatusCreated, s.toInfo(r.Context(), dst))
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
	// Bound the in-container inspection: a wedged container (e.g. an OOM'd or
	// hung agent) can make `docker exec git …` block indefinitely, which would
	// hang the purge call and freeze a TUI waiting on it. Cap it and, on timeout,
	// fail fast with a clear "use --force" rather than hanging forever.
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	git := s.gitStatusOf(ictx, p.ContainerName())
	if ictx.Err() != nil {
		return fmt.Errorf("island %q didn't respond to a workspace check within 5s "+
			"(the container may be wedged) — re-run with --force to purge anyway", p.Name)
	}
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

// agentRemovalRiskError returns a non-nil error describing at-risk work in an
// agent's worktree when removing it without --force would be unsafe, or nil when
// there is nothing to guard.
//
// This is the purge guard's sibling, and the asymmetry it closes was the bug:
// purging an island REFUSES on uncommitted work, while removing a single agent
// destroyed the same class of work silently — with no confirm, no flag and no
// guard — via `git worktree remove --force` in removeAgentSession. The careful
// gate was on the surface a human drives and the ungated one on the surface
// automation drives.
//
// It guards UNCOMMITTED work only, and that precision is the point rather than
// laziness. `git worktree remove --force` deletes the working directory; it does
// not delete the branch, so commits — pushed or not — survive in the shared
// repository and can be checked out again. Tested rather than reasoned:
// committed work is recoverable afterwards, uncommitted and untracked files are
// not. Warning about unpushed commits here would be crying wolf, and a guard
// that overstates the loss teaches people to pass --force by reflex.
func (s *Server) agentRemovalRiskError(ctx context.Context, p *project.Project, a *project.AgentSpec) error {
	// No worktree of its own means nothing is removed — removeAgentSession skips
	// the worktree step for "" and /workspace, so there is no loss to guard.
	if a.Worktree == "" || a.Worktree == "/workspace" {
		return nil
	}
	status, _ := s.rt.Status(ctx, p.ContainerName())
	if status != runtime.StatusRunning {
		return fmt.Errorf("island %q is not running, so agent %q's worktree can't be checked for "+
			"uncommitted work; wake it (`dejima wake %s`) to let the guard verify, "+
			"or re-run with --force to remove it anyway", p.Name, a.ID, p.Name)
	}
	// Bounded, for the same reason purgeRiskError bounds its check: a wedged
	// container must fail fast with an actionable message rather than hang a
	// client that is holding this island's lock.
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	git := s.gitStatusIn(ictx, p.ContainerName(), a.Worktree)
	if ictx.Err() != nil {
		return fmt.Errorf("agent %q's worktree didn't respond to a check within 5s "+
			"(the container may be wedged) — re-run with --force to remove it anyway", a.ID)
	}
	if git == nil || git.Clean || git.DirtyFiles == 0 {
		// Not a git worktree, or nothing uncommitted in it. Nothing to lose.
		return nil
	}
	branch := git.Branch
	if branch == "" {
		branch = "HEAD"
	}
	return fmt.Errorf("agent %q has %s in its worktree on branch %s — removing the agent "+
		"discards them permanently (its branch and commits are kept); commit them first, "+
		"or re-run with --force to remove it anyway",
		a.ID, countNoun(git.DirtyFiles, "uncommitted change"), branch)
}

// normalizeModel makes the model string forgiving: a bare model with no
// "provider/" prefix (e.g. "opus") gets the agent's provider prepended
// ("anthropic/opus"), so users don't have to repeat the provider. It does NOT
// validate or rewrite the model name itself — dejima keeps no model catalog (that
// would need constant updates); whatever survives is passed to the framework,
// which owns its own model vocabulary/aliases.
func normalizeModel(provider, model string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		return provider + "/" + model
	}
	return model
}

// sanitizeTags trims keys/values and drops entries with an empty key, so a
// malformed --tag can't persist a blank-keyed label. Returns nil when nothing
// survives (so an empty map isn't written to config).
func sanitizeTags(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
// oomPriorityExpendable is the smart default for a headless "brain" island
// (openclaw/Home): it self-restarts, so the OOM killer should sacrifice it before
// interactive work. Interactive islands default to 0 (normal).
const oomPriorityExpendable = -100

// oomScoreAdj maps an island's stack-rank priority (higher = more protected) to a
// docker --oom-score-adj (higher = killed first) — inverted and clamped to the
// kernel's −1000…+1000. The presets critical/+100, normal/0, expendable/−100 land
// at −500 / 0 / +500.
func oomScoreAdj(priority int) int {
	adj := -priority * 5
	if adj > 1000 {
		adj = 1000
	}
	if adj < -1000 {
		adj = -1000
	}
	return adj
}

// resolveOOMPriority is the effective priority for a new island: the explicit
// value when set, else expendable for a headless primary, else 0 (normal).
func resolveOOMPriority(explicit *int, primaryType string) int {
	if explicit != nil {
		return *explicit
	}
	if !handlers.Attachable(primaryType) {
		return oomPriorityExpendable
	}
	return 0
}

func ptrInt(i int) *int { return &i }

// oomScoreAdjPtr maps a stored priority to a CreateRequest.OOMScoreAdj: nil
// priority → nil (let the kernel default stand, no flag), else the mapped adj.
func oomScoreAdjPtr(priority *int) *int {
	if priority == nil {
		return nil
	}
	return ptrInt(oomScoreAdj(*priority))
}

func (s *Server) provision(ctx context.Context, name, repo, agent, image, cmd, role, ghIdentity string, res Resources, seedPath string, seedAgents []AgentSpecRequest, noRepo bool) (*project.Project, error) {
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
		NoRepo:         noRepo,
		Agent:          agent,
		Image:          image,
		Cmd:            cmd,
		Role:           role,
		GitHubIdentity: ghIdentity,
		Resources: project.Resources{
			Memory:      res.Memory,
			CPUs:        res.CPUs,
			Disk:        res.Disk,
			OOMPriority: ptrInt(resolveOOMPriority(res.OOMPriority, agent)),
		},
		DesiredState: project.StateRunning,
		CreatedAt:    now,
		LastUsedAt:   now,
		// Version-skew stamp: the daemon build this island's container is created
		// against. Compared later (doctor / ls / detail) to the running daemon to
		// flag an island built from a stale image whose /opt shims may be old.
		BuiltVersion: version.Version,
		// Born under the grant model, so the deny-by-default decision is already
		// made: no host gh credential unless the operator grants one. Without this
		// the load-time migration would grandfather every new island and the
		// default would never actually change.
		HostGitHubReviewed: true,
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
		// The primary's LLM provider/model (key-requiring frameworks) — Agents[0]
		// is synthesized from the scalar type, so carry the seed's choice across.
		p.Agents[0].Provider = strings.TrimSpace(seedAgents[0].Provider)
		p.Agents[0].Model = normalizeModel(seedAgents[0].Provider, seedAgents[0].Model)
		for _, ar := range seedAgents[1:] {
			spec, err := s.newAgentSpec(p, ar)
			if err != nil {
				return p, err
			}
			p.AddAgent(spec)
		}
	}
	// Give every agent (primary included) a meaningful, unique default label when
	// none was provided, so no surface falls back to the bare id. Idempotent and
	// only touches blank labels, so any seed-provided names above are preserved.
	p.BackfillAgentLabels()
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

	if err := s.createContainerForProject(ctx, p, seedPath, false); err != nil {
		return p, err
	}
	s.reconcileAgentsAsync(p, false) // bring up any non-primary agents once the clone lands
	return p, nil
}

// createContainerForProject creates the long-lived container for an existing
// project. Used by provision() and reset().
// resume, when true, launches the primary agent with its handler's ResumeLaunch
// (claude-code → `claude --continue`) instead of a cold Launch. Only the graceful
// operator-initiated paths pass true — recreating the container under a user who
// had a conversation going, where a cold start silently loses their context. A
// brand-new or reset island must NOT resume: there is nothing to continue, and
// `claude --continue` in an empty state dir is at best a no-op.
func (s *Server) createContainerForProject(ctx context.Context, p *project.Project, seedPath string, resume bool) error {
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
	// Egress proxy (Phase 1, observe-first): route the island's outbound HTTP(S)
	// through the daemon's proxy so destinations are visible. Attribution is the
	// island name (the proxy username); NO_PROXY exempts the autonomy/telemetry
	// path + loopback. Off (no env) unless EnableEgress was called.
	if s.egressDial != "" {
		for k, v := range egress.ProxyEnv(p.Name, s.egressDial) {
			env[k] = v
		}
	}
	// Everything the entrypoint needs about the primary agent flows via env, so
	// the launch command lives in one place (the handler registry) rather than
	// being duplicated in start.sh. Non-primary agents are launched by the daemon
	// (reconcileAgents), each overriding DEJIMA_AGENT_ID per session.
	agentType := p.Agent
	if pa := p.PrimaryAgent(); pa != nil {
		agentType = pa.Type
		env["DEJIMA_AGENT_ID"] = pa.ID
		// Fall back to the SAME default the attach path uses. These two disagreed:
		// sessionWS defaults an empty Tmux to "agent-"+ID, while start.sh defaults
		// an empty DEJIMA_TMUX to the literal "agent-a1" — a stale first-agent id
		// that is wrong for every island whose agents are not named a1.
		//
		// So on a project record with no Tmux (one created before the field was
		// populated), the entrypoint launches the agent into "agent-a1" and the
		// daemon attaches to "agent-<id>", which does not exist. `tmux new-session
		// -A` then CREATES it, and the operator gets a bare shell where their agent
		// should be, while the real agent runs on in a session nothing points at.
		// Two defaults for one name is the bug; this makes the daemon always send a
		// value so the fallback in start.sh can never be reached.
		env["DEJIMA_TMUX"] = pa.Tmux
		if pa.Tmux == "" {
			env["DEJIMA_TMUX"] = "agent-" + pa.ID
		}
		if pa.Cmd != "" {
			env["DEJIMA_AGENT_CMD"] = pa.Cmd
		}
		if h, ok := handlers.Lookup(pa.Type); ok {
			// LaunchFor falls back to the plain Launch when the handler has no resume
			// affordance, and both are empty for headless → entrypoint runs
			// DEJIMA_AGENT_CMD as PID 1.
			env["DEJIMA_LAUNCH"] = h.LaunchFor(resume)
			// LLM provider/model selection for frameworks that reach a model over
			// an API key. The key bytes are NOT injected here — only the path to
			// the read-only mounted file is (materialized by islandLLMConfigDir),
			// so the secret never appears in env / `docker inspect`. The per-agent
			// shim sources the file and translates DEJIMA_MODEL into native config.
			if h.RequiresProviderKey {
				if pa.Model != "" {
					env["DEJIMA_MODEL"] = pa.Model
				}
				if store, err := providercreds.Load(); err == nil {
					if prov, ok := store.Resolve(pa.Provider); ok {
						env["DEJIMA_PROVIDER"] = prov.Name
						env["DEJIMA_PROVIDER_KEY_FILE"] = "/opt/host/llm/" + prov.Name + ".env"
					}
				}
			}
		}
	} else {
		// No agents (all removed, or seeded with none): the entrypoint just keeps
		// the container alive so you can shell in or add agents later, instead of
		// erroring on a missing launch command. See image/start.sh.
		env["DEJIMA_AGENTLESS"] = "1"
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
	if s.autonomyDial != "" || s.egressDial != "" {
		// Both the autonomy path and the egress proxy are reached via
		// host.docker.internal; ensure it resolves on engines that don't provide
		// it. (Added once even when both are set.)
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
		OOMScoreAdj: oomScoreAdjPtr(p.Resources.OOMPriority),
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
func (s *Server) reconcileAgentsAsync(p *project.Project, resume bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.reconcileAgents(ctx, p, resume); err != nil {
			s.log.Warn("reconcile agents", "island", p.Name, "err", err)
		}
	}()
}

// reconcileAgents brings tmux sessions and worktrees into line with p.Agents for
// every non-primary agent. Idempotent. The primary (Agents[0]) is launched by
// the entrypoint and skipped here.
// resume is passed through to each session so the non-primary agents follow the
// same cold/continue decision the primary got — otherwise an upgrade would
// resume agent 0 and cold-start the rest, which is worse than being consistent.
func (s *Server) reconcileAgents(ctx context.Context, p *project.Project, resume bool) error {
	if len(p.Agents) <= 1 {
		return nil
	}
	if !s.waitForWorkspace(ctx, p) {
		return fmt.Errorf("workspace not ready for %q", p.Name)
	}
	for i := 1; i < len(p.Agents); i++ {
		a := &p.Agents[i]
		if err := s.ensureAgentSession(ctx, p, a, resume); err != nil {
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
func (s *Server) ensureAgentSession(ctx context.Context, p *project.Project, a *project.AgentSpec, resume bool) error {
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
	// Install this co-located agent's per-type shim BEFORE launching it. start.sh
	// runs the shim only for the PRIMARY agent, so an agent whose type's init.sh
	// never ran — a different type than the primary, or any agent added after boot
	// — would otherwise launch with no agent-state hook wired into the shared
	// ~/.claude. A missing heartbeat silently disables wake-on-message, idle
	// auto-hibernate, and the idle metric for that agent (the recipient never sees
	// a delivered cross-island message). The shim is idempotent, so re-running it
	// is safe.
	s.runAgentShim(ctx, p, a)
	// Both interactive and headless agents run inside a tmux session (the host
	// process), scoped to DEJIMA_AGENT_ID via sh so we don't depend on a specific
	// tmux version's `new-session -e`. Headless agents are marked non-attachable,
	// redirect to a per-agent log file, and optionally restart on crash.
	script := agentLaunchScript(a, resume)
	_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{
		"tmux", "new-session", "-d", "-s", a.Tmux, "-c", wt, "sh", "-c", script,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("tmux new-session for %q: %s", a.ID, strings.TrimSpace(stderr))
	}
	// Stamp this agent's identity into the tmux SESSION environment so ANY shell
	// later spawned in the pane — most importantly a human running `claude --resume`
	// by hand — inherits the correct per-agent DEJIMA_AGENT_ID, not the container-
	// wide PRIMARY id (server.go sets DEJIMA_AGENT_ID globally to the primary; the
	// inline `DEJIMA_AGENT_ID=X exec …` in agentLaunchScript only scopes the launch
	// process itself). Without this, a manual resume in a non-primary pane polls the
	// primary's mailbox — the id clobber we hit live. `set-environment` is the
	// version-portable equivalent of `new-session -e`; best-effort (a failure just
	// leaves the inline-scoped launch as-is).
	s.setSessionAgentEnv(ctx, p, a)
	return nil
}

// restartAgentSession relaunches ONE agent in place: it kills the agent's tmux
// session and re-creates it, so the agent starts in a fresh login shell and picks
// up any changed environment (a newly added or rotated secret). resume, when the
// handler supports it (claude-code → `claude --continue`), continues the previous
// conversation instead of a cold start. This is the lighter, per-agent
// alternative to a whole-container recreate for an island that already carries
// its secrets mount.
func (s *Server) restartAgentSession(ctx context.Context, p *project.Project, a *project.AgentSpec, resume bool) error {
	if a.Tmux != "" {
		// Best-effort kill; if the session is already gone, ensureAgentSession just
		// creates a fresh one below.
		_, _, _, _ = s.rt.Exec(ctx, p.ContainerName(), []string{"tmux", "kill-session", "-t", a.Tmux})
	}
	return s.ensureAgentSession(ctx, p, a, resume)
}

// restartAgent relaunches one agent in place so it picks up a changed
// environment — e.g. a newly added/rotated secret. Optional {"resume": true}
// continues the agent's prior conversation when the framework supports it.
// Operator-only; the island must be running (tmux lives in the container).
func (s *Server) restartAgent(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		Resume bool `json:"resume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if err := s.restartAgentSession(r.Context(), p, a, req.Resume); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resumed := req.Resume
	if h, ok := handlers.Lookup(a.Type); !ok || h.ResumeLaunch == "" {
		resumed = false // asked to resume, but this framework has no resume affordance
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarted": id, "resumed": resumed})
}

// setSessionAgentEnv records the per-agent DEJIMA_AGENT_ID / DEJIMA_TMUX in the
// agent's tmux session environment so shells spawned in its pane resolve the
// right agent. Best-effort and idempotent.
func (s *Server) setSessionAgentEnv(ctx context.Context, p *project.Project, a *project.AgentSpec) {
	for _, kv := range [][2]string{{"DEJIMA_AGENT_ID", a.ID}, {"DEJIMA_TMUX", a.Tmux}} {
		_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(),
			[]string{"tmux", "set-environment", "-t", a.Tmux, kv[0], kv[1]})
		if err != nil || code != 0 {
			s.log.Debug("set tmux session env", "island", p.Name, "agent", a.ID,
				"key", kv[0], "code", code, "err", err, "stderr", strings.TrimSpace(stderr))
		}
	}
}

// runAgentShim runs a co-located agent's per-type init.sh inside the container —
// the same shim start.sh runs for the primary — so the agent's hooks/creds/
// template land in the shared ~/.claude before it launches. DEJIMA_AGENT_ID is
// scoped to this agent so any per-agent shim logic targets it. Best-effort: a
// type with no shim is a clean no-op, and a shim failure is logged but must not
// block the launch (the agent still runs; it just may lack the heartbeat hook).
func (s *Server) runAgentShim(ctx context.Context, p *project.Project, a *project.AgentSpec) {
	shim := "/opt/dejima/agents/" + a.Type + "/init.sh"
	// This agent's worktree, so init.sh can pre-accept Claude Code's per-project
	// trust/onboarding for it — each agent runs in its own worktree, which Claude
	// otherwise treats as a new untrusted project and re-prompts on. Empty → the
	// primary's /workspace. Single-quoted since a worktree path is daemon-derived.
	wt := a.Worktree
	if wt == "" {
		wt = "/workspace"
	}
	// `[ -x ] || exit 0` keeps a missing shim (unknown type / stale image) silent.
	cmd := "[ -x " + shim + " ] || exit 0; exec " + shim
	_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(),
		[]string{"sh", "-c", "DEJIMA_AGENT_ID=" + a.ID + " DEJIMA_WORKTREE='" + wt + "' " + cmd})
	if err != nil || code != 0 {
		s.log.Warn("agent shim", "island", p.Name, "agent", a.ID, "type", a.Type,
			"code", code, "err", err, "stderr", strings.TrimSpace(stderr))
	}
}

// headlessLogPath is the per-agent log file for a co-located headless agent.
func headlessLogPath(agentID string) string {
	return "/home/dejima/.dejima/agents/" + agentID + ".log"
}

// agentLaunchScript builds the sh -c script that a tmux session runs for one
// agent. Interactive agents exec their launch command directly; headless agents
// redirect output to a per-agent log file and (when Restart) self-respawn.
// shSingleQuote wraps s in single quotes for safe inclusion in a /bin/sh command,
// escaping embedded single quotes (the '\” idiom).
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func agentLaunchScript(a *project.AgentSpec, resume bool) string {
	idEnv := "DEJIMA_AGENT_ID=" + a.ID + " "
	if handlers.Attachable(a.Type) {
		h, _ := handlers.Lookup(a.Type)
		launch := h.LaunchFor(resume) // resume continues the prior conversation when supported
		if launch == "" {
			launch = a.Type // unknown/custom interactive agent: run the type as a command
		}
		// Source the island's Dejima-managed secrets into the agent's environment
		// before exec, so they reach the agent AND every tool subprocess it spawns
		// (its own Bash tool is a non-login shell, so inheriting from the agent
		// process is the only path). Secrets normally load via /etc/profile.d, but
		// ONLY for login shells — and the tmux session runs this under a non-login
		// `sh -c`, so the agent otherwise never sees them (the exact "my secret
		// isn't there" bug). We source the profile.d hook directly under `bash -c`
		// (NOT `bash -lc`): a login shell would re-run /etc/profile and reset PATH,
		// dropping /opt/dejima/npm-global/bin where the agent binary lives. bash
		// (not sh) so load-secrets' %q-quoted output evals correctly. A missing hook
		// (older image) is a harmless no-op; headless agents wrap their own bash -lc.
		inner := ". /etc/profile.d/10-dejima-secrets.sh 2>/dev/null || true; exec " + launch
		return idEnv + "exec bash -c " + shSingleQuote(inner)
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
		// Supervise-and-respawn with bounded exponential backoff (2s→…→60s) so a
		// crash-looping agent (e.g. repeatedly OOM-killed) doesn't hammer the host
		// every few seconds. Backoff resets once the process has run a healthy
		// while (≥30s), so a long-lived agent that finally dies restarts promptly.
		// The exit marker is headlessRestartMarker(id) — counted by the daemon to
		// surface Restarts in the TUI. POSIX sh ($((…)), $(date +%s), [ ]).
		return fmt.Sprintf(
			"exec >> %s 2>&1; n=0; delay=2; "+
				"while true; do start=$(date +%%s); %s%s; code=$?; end=$(date +%%s); n=$((n+1)); "+
				"if [ $((end-start)) -ge 30 ]; then delay=2; fi; "+
				"echo \"[dejima] agent %s exited ($code); restart #$n in ${delay}s\"; "+
				"sleep \"$delay\"; delay=$((delay*2)); if [ \"$delay\" -gt 60 ]; then delay=60; fi; done",
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
		// Name the cause when it's by design. Identical symptom, opposite
		// meaning: a repo-less island isn't missing a checkout, it never had
		// one, and "no repo at /workspace" alone reads as a clone that failed.
		if p.NoRepo {
			return fmt.Errorf("island %q was created with --no-repo, so there's no /workspace repo to base a worktree on; co-located agents on a repo-less island share /workspace directly", p.Name)
		}
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

// agentLiveness classifies an agent's session: "stopped" (no tmux session),
// "exited" (session alive but its foreground fell back to a bare shell — the
// agent process died while start.sh kept the container up), or "running". The
// "exited" verdict never applies to the shell agent type (whose foreground IS a
// shell), nor to a supervised (Restart) agent: its supervisor loop legitimately
// cycles the pane through a shell and `sleep` backoff between respawns, so a
// momentary shell foreground is normal, not death. A supervised agent that's
// actually crash-looping shows up via its climbing Restarts count, not a false
// "exited". Best-effort heuristic via the tmux pane command.
func (s *Server) agentLiveness(ctx context.Context, p *project.Project, a *project.AgentSpec) string {
	ok, _ := s.tmuxHasSession(ctx, p, a.Tmux)
	if !ok {
		return "stopped"
	}
	if a.Type == handlers.Shell || a.Restart {
		return "running" // shell prompt / supervised loop are both the healthy state
	}
	out, _, code, err := s.rt.Exec(ctx, p.ContainerName(),
		[]string{"tmux", "display-message", "-p", "-t", a.Tmux, "#{pane_current_command}"})
	if err != nil || code != 0 {
		return "running" // can't tell; don't cry wolf
	}
	if isLoginShell(strings.TrimSpace(out)) {
		return "exited"
	}
	return "running"
}

// headlessRestartCount counts how many times a supervised headless agent has
// crashed and respawned, by counting the supervisor's exit markers in the
// per-agent log (grep -F: the marker contains regex metacharacters). 0 on any
// error — a missing count must never read as a problem.
func (s *Server) headlessRestartCount(ctx context.Context, p *project.Project, id string) int {
	out, _, code, err := s.rt.Exec(ctx, p.ContainerName(),
		[]string{"grep", "-cF", headlessRestartMarker(id), headlessLogPath(id)})
	if err != nil || code != 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// headlessRestartMarker is the literal prefix the supervisor loop logs on each
// crash — the single source of truth for both writing (agentLaunchScript) and
// counting (headlessRestartCount).
func headlessRestartMarker(id string) string { return "[dejima] agent " + id + " exited" }

// isLoginShell reports whether a tmux pane_current_command names a bare shell —
// i.e. the agent's foreground process is gone and only a prompt remains.
func isLoginShell(cmd string) bool {
	switch strings.TrimPrefix(cmd, "-") {
	case "bash", "sh", "zsh", "dash", "ash", "fish":
		return true
	}
	return false
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
	// Same for the materialized LLM provider keys (plaintext .env files).
	if dir, err := paths.LLMIslandConfigPath(p.Name); err == nil {
		_ = os.RemoveAll(dir)
	}
	// Per-island secrets: values (keychain entries) AND the metadata + the
	// materialized mount file. Scoped to the island, so they must not outlive
	// it — and keychain entries would otherwise persist with nothing pointing
	// at them, invisible to every surface.
	if store, err := secrets.OpenIsland(); err == nil {
		_ = store.Purge(p.Name)
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
		if err := s.createContainerForProject(r.Context(), p, "", false); err != nil {
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
	// Match whatever the ENTRYPOINT is about to do with the primary, rather than
	// asserting a value here. A container upgraded earlier carries
	// DEJIMA_LAUNCH="claude --continue" permanently, so a hardcoded false here
	// resumed the primary and cold-started everyone else — the exact split the
	// upgrade fix exists to prevent, one hibernate/wake cycle later.
	s.reconcileAgentsAsync(p, s.containerResumesPrimary(r.Context(), p))
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
	if err := s.createContainerForProject(r.Context(), p, "", false); err != nil {
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
	//
	// resume=true: an upgrade is the textbook graceful, operator-initiated
	// restart ResumeLaunch exists for. The operator is recreating the container
	// under agents that were mid-conversation, and their state dirs (~/.claude et
	// al) live on the persisted volume — so the transcript is right there and a
	// cold start would strand it.
	if err := s.createContainerForProject(r.Context(), p, "", true); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Honor the prior desired state, as reset does.
	leftStopped := false
	if !wasRunning && p.DesiredState == project.StateHibernated {
		_ = s.rt.StopContainer(r.Context(), p.ContainerName())
		leftStopped = true
	}

	p.LastUsedAt = time.Now().UTC()
	// Re-stamp the version skew marker: the container was just recreated against
	// the current image, so the island is now level with this daemon build (and
	// its /opt shims are fresh). Back-fill BuiltVersion too for islands created
	// before the stamp existed, so provenance is no longer "unknown" after upgrade.
	p.UpgradedVersion = version.Version
	if p.BuiltVersion == "" {
		p.BuiltVersion = version.Version
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.emit(events.Event{Type: events.TypeIslandUpgraded, Island: p.Name})
	// The entrypoint relaunches only the PRIMARY agent; the rest are the daemon's
	// job. wakeIsland has always done this and upgrade never did, so upgrading a
	// multi-agent island silently brought back agent 0 alone and left the others
	// with no tmux session at all. Skipped when the island was deliberately left
	// hibernated above — there is no running container to create sessions in, and
	// the next wake reconciles anyway.
	if !leftStopped {
		s.reconcileAgentsAsync(p, true)
	}
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

	// Pin the in-island CLI to THIS daemon's release. Two reasons, both load-bearing:
	//
	//  1. Cache correctness. The Dockerfile's default is DEJIMA_VERSION=latest,
	//     which it resolves by curl-ing the GitHub releases API *inside* a RUN.
	//     Docker can't see that the answer changed, so it reuses that layer
	//     forever — the in-island `dejima` froze at whatever release was newest
	//     the first time the layer built, and no number of rebuilds moved it.
	//     Passing an explicit version changes the ARG, which invalidates the layer.
	//  2. Version agreement. An island's CLI talks to this daemon; building it
	//     from "whatever GitHub calls latest" could straddle a release boundary
	//     mid-build and hand an island a client newer than the daemon it reports to.
	//
	// A dev/source daemon has no matching published release, so it keeps the
	// "latest" default (there is no asset named dejima_dev_*.tar.gz to fetch) —
	// and with it the stale-layer caveat, which only bites un-released builds.
	// IsExactRelease, not IsRelease: the latter also accepts a git-describe string
	// like "v0.8.60-3-gabc1234", and no release asset exists under that name — the
	// Dockerfile's curl would 404 and fail the whole build.
	buildArgs := map[string]string{}
	if version.IsExactRelease(version.Version) {
		buildArgs["DEJIMA_VERSION"] = version.Version
	}
	stream, err := s.rt.BuildImage(r.Context(), dir, islandimage.Dockerfile, DefaultImage, buildArgs)
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
		Name:        p.Name,
		Title:       p.Title,
		Repo:        p.RepoURL,
		NoRepo:      p.NoRepo,
		Agent:       agentType,
		Image:       p.Image,
		Cmd:         cmd,
		Role:        p.Role,
		Owner:       p.Owner,
		Tags:        p.Tags,
		State:       string(p.DesiredState),
		NoHibernate: p.NoHibernate,
		CreatedAt:   p.CreatedAt,
		LastUsedAt:  p.LastUsedAt,
	}
	// Health surface (v1b): an island whose NAMED GitHub identity no longer
	// resolves for its tenant will fail to clone/push — flag it so it's not a
	// silent break. Cheap (a small local store read); only when an identity is
	// explicitly named, so public-repo islands aren't false-flagged.
	if id := strings.TrimSpace(p.GitHubIdentity); id != "" {
		if store, err := githubid.Load(); err == nil {
			if _, ok := store.ResolveForIsland(ghOwner(p.Owner), id); !ok {
				info.GitHubCredMissing = true
			}
		}
	}
	// Host-gh grant state, so a host island that now has NO GitHub credential
	// says so here — the same surface a tenant island uses — instead of failing
	// opaquely at the first clone/push. It also carries the Grandfathered flag,
	// which is how "islands still on the old inherited credential" stays a
	// question with an answer after the deny-by-default migration.
	if v := hostGitHubView(p); v.Eligible {
		info.GitHubHostCredential = &v
	}
	// Secret COUNT for the dashboard's per-island row. Reads the island's
	// metadata file only — never the keychain — so it stays cheap enough for the
	// poll and can't trigger a keychain access prompt.
	if store, err := secrets.OpenIsland(); err == nil {
		if metas, lerr := store.List(p.Name); lerr == nil {
			info.SecretsCount = len(metas)
		}
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
	// Configured resource caps + OOM priority. Cheap (read from the island's
	// config, no container probe), so surfaced on both the list and detail views
	// — the consumer needs the caps to compute "% of cap" client-side from the
	// cached usage stats above.
	info.Resources = &Resources{
		Memory:      p.Resources.Memory,
		CPUs:        p.Resources.CPUs,
		Disk:        p.Resources.Disk,
		OOMPriority: p.Resources.OOMPriority,
	}
	info.Attached = s.islandPresence(p.Name)
	info.AgentState = s.islandAgentState(p.Name)
	info.Agents = s.agentInfos(ctx, p, false)
	info.GitHubIdentity = p.GitHubIdentity
	info.BuiltVersion = p.BuiltVersion
	info.UpgradedVersion = p.UpgradedVersion
	// Zero-heartbeat liveness: a running island that has never emitted a single
	// agent-state event, past a short grace window, is the direct broken-shim
	// signal (a stale socket→TCP notify hook no-ops silently). We use the rollup
	// AgentState (any agent suffices); LastUsedAt is the most recent
	// boot/recreate/upgrade time available without a per-container engine probe.
	running := info.Container == string(runtime.StatusRunning)
	info.NeverHeardFrom = neverHeardFrom(running, info.AgentState, p.LastUsedAt, time.Now())
	// Operator-set visual identity override (color + glyph). Omitted from the
	// payload when unset, so the TUI falls back to its deterministic per-name
	// default. Set/cleared via PUT/DELETE /v1/islands/{name}/identity (identity.go).
	if p.Identity.IsSet() {
		info.Identity = &IslandIdentity{Color: p.Identity.Color, Glyph: p.Identity.Glyph}
	}
	return info
}

// heartbeatGrace is how long a freshly (re)started island is given to emit its
// first agent-state heartbeat before a continued silence is treated as a broken
// shim. Generous enough to cover a slow clone + agent warm-up, short relative to
// the 18h the motivating incident went unnoticed.
const heartbeatGrace = 10 * time.Minute

// neverHeardFrom decides the zero-heartbeat liveness flag: a running island that
// has emitted NO agent-state event (agentState nil) and whose last
// boot/recreate (sinceTime, from LastUsedAt) is older than heartbeatGrace. A
// just-started island, a hibernated one, or one with a zero reference time is
// never flagged. Pure, so the grace logic is unit-tested without a runtime.
func neverHeardFrom(running bool, agentState *AgentStateInfo, sinceTime, now time.Time) bool {
	if !running || agentState != nil || sinceTime.IsZero() {
		return false
	}
	return now.Sub(sinceTime) > heartbeatGrace
}

// agentInfos builds the per-agent public view. When live is true, each agent's
// tmux session liveness is probed (one container exec per agent) — detail-only,
// since the list view refreshes frequently and would multiply the exec cost.
func (s *Server) agentInfos(ctx context.Context, p *project.Project, live bool) []AgentInfo {
	out := make([]AgentInfo, 0, len(p.Agents))
	for i := range p.Agents {
		a := &p.Agents[i]
		ai := AgentInfo{
			// Stamped HERE, not read from the record. This function's whole
			// premise is that it is enumerating one island's agents, so being in
			// an island is a fact it knows and the stored agent does not. A record
			// that carried its own level could drift from where it actually lives.
			Containment: ContainmentContained,
			ID:          a.ID,
			Type:        a.Type,
			Label:       a.Label,
			Tmux:        a.Tmux,
			Branch:      a.Branch,
			Worktree:    a.Worktree,
			Attachable:  handlers.Attachable(a.Type),
			CreatedAt:   a.CreatedAt,
			Ephemeral:   a.Ephemeral,
			SpawnedBy:   a.SpawnedBy,
		}
		// Resolve the spawner's name from the same roster so lineage renders as a
		// name, not a bare id.
		if a.SpawnedBy != "" {
			if parent, ok := p.AgentByID(a.SpawnedBy); ok {
				ai.SpawnedByLabel = parent.Label
			}
		}
		if live && a.Tmux != "" {
			ai.State = s.agentLiveness(ctx, p, a)
			if a.Restart && !handlers.Attachable(a.Type) {
				ai.Restarts = s.headlessRestartCount(ctx, p, a.ID)
			}
		}
		ai.Attached = s.presenceSnapshot(p.Name, a.ID)
		ai.AgentState = s.agentStateOf(p.Name, a.ID)
		ai.Usage = s.agentUsageOf(p.Name, a.ID)
		if ai.AgentState == nil && i == 0 {
			// Surface legacy agent-less events (no DEJIMA_AGENT_ID) on the primary.
			ai.AgentState = s.agentStateOf(p.Name, "")
		}
		if ai.Usage == nil && i == 0 {
			// Same legacy fallback: usage reported with no DEJIMA_AGENT_ID.
			ai.Usage = s.agentUsageOf(p.Name, "")
		}
		if msg, at, ok := s.agentErrorOf(p.Name, a.ID); ok {
			ai.Error, ai.ErrorAt = msg, at
		}
		ai.Provider, ai.Model, ai.ProviderKeySet, ai.AuthState = agentProviderStatus(a)
		out = append(out, ai)
	}
	return out
}

// agentProviderStatus reports an agent's LLM provider/model and whether dejima
// has a key to inject — computed purely from the handler registry + the provider
// store (no log-scraping). For a key-requiring handler with no resolvable key it
// returns authState "missing-provider-auth", the proactive signal that the agent
// will fail at first task; for OAuth-seeded / non-LLM handlers it returns an
// empty authState (the subsystem doesn't apply).
func agentProviderStatus(a *project.AgentSpec) (provider, model string, keySet bool, authState string) {
	h, ok := handlers.Lookup(a.Type)
	if !ok || !h.RequiresProviderKey {
		return "", a.Model, false, ""
	}
	if store, err := providercreds.Load(); err == nil {
		if prov, ok := store.Resolve(a.Provider); ok {
			return prov.Name, a.Model, true, ""
		}
	}
	return strings.TrimSpace(a.Provider), a.Model, false, "missing-provider-auth"
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
	id, ok := store.ResolveForIsland(ghOwner(p.Owner), p.GitHubIdentity)
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
	// config.yml carries the schema version marker so gh treats the config as
	// already-migrated and never writes to the read-only mount (see HostsYAML).
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(githubid.ConfigYAML()), 0o600); err != nil {
		return "", fmt.Errorf("write island gh config.yml: %w", err)
	}
	// The container runs as uid 1000 and cannot read a root-owned 0600 file in a
	// 0700 directory. Without this the island has its credential mounted and
	// unreadable, and every private clone fails as an auth error.
	makeIslandReadableTree(dir)
	return dir, nil
}

// islandGitConfig materializes the per-island commit-author gitconfig for the
// island's selected GitHub identity and returns the file to mount at
// /opt/host/gitconfig. Returns "" (no error) when the island resolves no
// identity, so the caller falls back to the host's own gitconfig. Authoring
// commits as the identity (its noreply email) is what makes GitHub attribute
// them to the right account — otherwise the push authenticates as the identity
// but the commit carries the daemon host's gitconfig user.*. Co-located with the
// gh config so island teardown (GitHubIslandConfigPath) removes it too.
func islandGitConfig(p *project.Project) (string, error) {
	store, err := githubid.Load()
	if err != nil {
		return "", fmt.Errorf("load github identities: %w", err)
	}
	id, ok := store.ResolveForIsland(ghOwner(p.Owner), p.GitHubIdentity)
	if !ok {
		return "", nil
	}
	dir, err := paths.GitHubIslandConfigDir(p.Name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(githubid.GitConfig(id)), 0o600); err != nil {
		return "", fmt.Errorf("write island gitconfig: %w", err)
	}
	makeIslandReadable(path)
	return path, nil
}

// islandLLMConfigDir materializes the per-island LLM provider .env file(s) and
// returns the dir to mount read-only at /opt/host/llm. For each distinct
// provider referenced by the island's key-requiring agents (AgentSpec.Provider,
// or the store default when blank), it writes a single-provider <name>.env
// (0600) the per-agent shim sources, plus a key-less providers.json manifest.
// Returns "" (no error) when no provider resolves — an island that needs no LLM
// key still boots, and the missing-key state surfaces via agent health. The
// files hold plaintext keys, so teardown removes the dir (LLMIslandConfigPath).
func islandLLMConfigDir(p *project.Project) (string, error) {
	store, err := providercreds.Load()
	if err != nil {
		return "", fmt.Errorf("load provider credentials: %w", err)
	}
	seen := map[string]providercreds.Provider{}
	for i := range p.Agents {
		a := &p.Agents[i]
		if h, ok := handlers.Lookup(a.Type); !ok || !h.RequiresProviderKey {
			continue
		}
		if prov, ok := store.Resolve(a.Provider); ok {
			seen[prov.Name] = prov
		}
	}
	dir, err := paths.LLMIslandConfigDir(p.Name)
	if err != nil {
		return "", err
	}
	// Prune first. This function is also the REFRESH path, so it runs against a
	// dir that may already hold a previous resolution's files — and a provider
	// the island no longer resolves must have its key REMOVED, not merely
	// stopped being rewritten. Leaving it is the silent-revoke shape: `dejima
	// provider rm` reports success, the store is clean, and the island keeps a
	// working copy of the revoked key plus a manifest still advertising it.
	if err := pruneIslandLLMConfig(dir, seen); err != nil {
		return "", err
	}
	if len(seen) == 0 {
		return "", nil
	}
	manifest := make([]providercreds.Meta, 0, len(seen))
	for _, prov := range seen {
		if err := os.WriteFile(filepath.Join(dir, prov.Name+".env"), []byte(providercreds.DotEnv(prov)), 0o600); err != nil {
			return "", fmt.Errorf("write island llm env: %w", err)
		}
		// Manifest carries non-secret descriptors only (name/env-var/base-url).
		manifest = append(manifest, providercreds.Meta{Name: prov.Name, EnvVar: providercreds.EnvVarName(prov), BaseURL: prov.BaseURL})
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), b, 0o600); err != nil {
		return "", fmt.Errorf("write island llm manifest: %w", err)
	}
	makeIslandReadableTree(dir)
	return dir, nil
}

// pruneIslandLLMConfig deletes materialized provider keys the island no longer
// resolves. keep is the set that survives; anything else goes, and when keep is
// empty so does the manifest — an island with no provider must be left with no
// key material at all, matching what LLMIslandConfigPath teardown promises.
//
// A missing dir is not an error: nothing to prune is the success case.
func pruneIslandLLMConfig(dir string, keep map[string]providercreds.Provider) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read island llm dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".env") {
			continue
		}
		if _, ok := keep[strings.TrimSuffix(name, ".env")]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale island llm env: %w", err)
		}
	}
	if len(keep) == 0 {
		if err := os.Remove(filepath.Join(dir, "providers.json")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale island llm manifest: %w", err)
		}
	}
	return nil
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
			HostPath: dir, ContainerPath: GitHubCredentialMountPath, ReadOnly: true,
		})
	} else if ghOwner(p.Owner) == "" && p.HostGitHubAllowed() {
		// The host's own ~/.config/gh is a HOST-island-only fallback, and now also
		// an EXPLICITLY GRANTED one. A tenant island that resolves no identity gets
		// no gh credential (surfaced via health + the self-serve "connect GitHub"
		// prompt) rather than silently inheriting the host operator's login; a host
		// island gets it only where the operator granted it, because that login
		// reads the operator's whole account and an island may hold several
		// autonomous agents. See project/github_host.go for the grant model and the
		// migration that converted the old silent inheritance into explicit grants.
		if ghDir, err := paths.HostGHConfigDir(); err == nil {
			if _, statErr := os.Stat(ghDir); statErr == nil {
				binds = append(binds, runtime.BindMount{
					HostPath: ghDir, ContainerPath: GitHubCredentialMountPath, ReadOnly: true,
				})
			}
		}
	}

	// Per-island secrets: a KEY=VALUE file the island PARSES (never sources).
	// The DIRECTORY is mounted, not the file — a file bind binds the inode, and
	// the file is replaced by rename, so the container would read the original
	// inode forever. See island_secrets.go.
	if secretsPath, err := islandSecretsMount(p); err != nil {
		return nil, err
	} else if secretsPath != "" {
		binds = append(binds, runtime.BindMount{
			HostPath: secretsPath, ContainerPath: secretsMountPath, ReadOnly: true,
		})
		// And the old file path alongside it, for an island image built before
		// secrets.d existed — see legacySecretsMountPath. Without this, updating
		// the daemon without rebuilding the image makes every secret vanish
		// rather than merely go stale.
		binds = append(binds, runtime.BindMount{
			HostPath:      filepath.Join(secretsPath, secretsFileName),
			ContainerPath: legacySecretsMountPath, ReadOnly: true,
		})
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

	// Git author: an island bound to a GitHub identity authors commits as that
	// identity (its noreply email) so GitHub attributes them correctly. The
	// materialized gitconfig overrides the host gitconfig at the same mount point;
	// only when no identity is selected do we fall back to the host's own.
	if gc, err := islandGitConfig(p); err != nil {
		return nil, err
	} else if gc != "" {
		binds = append(binds, runtime.BindMount{
			HostPath: gc, ContainerPath: "/opt/host/gitconfig", ReadOnly: true,
		})
	} else if gitConfig, err := paths.HostGitConfig(); err == nil {
		if _, statErr := os.Stat(gitConfig); statErr == nil {
			binds = append(binds, runtime.BindMount{
				HostPath: gitConfig, ContainerPath: "/opt/host/gitconfig", ReadOnly: true,
			})
		}
	}

	// LLM provider keys: materialize the per-island <provider>.env file(s) for
	// the agents' chosen providers and mount them read-only at /opt/host/llm. The
	// key bytes live only in this 0600 file — never a container env var, so never
	// in `docker inspect` or logs. The per-agent shim sources it before launch.
	if dir, err := islandLLMConfigDir(p); err != nil {
		return nil, err
	} else if dir != "" {
		binds = append(binds, runtime.BindMount{
			HostPath: dir, ContainerPath: "/opt/host/llm", ReadOnly: true,
		})
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
