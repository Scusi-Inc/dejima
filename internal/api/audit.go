package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/ledger"
)

// This file is Lane 1's operational audit layer. It builds on the already-shipped
// tamper-evident, hash-chained ledger (internal/ledger, written host-side at
// ~/.dejima/ledger.jsonl) that records brokered Port/Trade/capability operations.
// On top of that always-on substrate it adds an OPT-IN operational record —
// API requests and governance-relevant lifecycle events — plus a read/filter/
// export surface over the whole ledger. Verification always covers the complete
// chain; filters only change which entries the caller is handed back.

// auditTypeAPIRequest is the ledger Type for an operational API-request record.
// Lifecycle records reuse the event vocabulary verbatim (e.g. "island.created").
const auditTypeAPIRequest = "api.request"

// AuditOptions configures the operational audit log.
type AuditOptions struct {
	// Reads also records read (GET/HEAD/OPTIONS) requests. Default (false)
	// records state-changing requests + lifecycle only, keeping the log lean and
	// free of high-volume TUI polling noise.
	Reads bool
}

// EnableAudit turns on the operational audit log: api.request + lifecycle
// records are appended to the hash-chained ledger. Off by default; dejimad calls
// this when started with --audit. The HMAC keying of the ledger file itself is a
// separate, ledger-wide setting (ledger.Configure), applied before the first
// append; this only governs whether the operational layer is recorded at all.
func (s *Server) EnableAudit(opts AuditOptions) {
	s.auditEnabled = true
	s.auditReads = opts.Reads
}

// AuditEnabled reports whether the operational audit log is on.
func (s *Server) AuditEnabled() bool { return s.auditEnabled }

// --- identity seam (Lane 2) ------------------------------------------------

// AuditIdentity is the authenticated caller (who + role) attached to a request
// by the auth layer. Lane 2 (token auth / roles) populates it via
// WithAuditIdentity; the audit middleware records it as the entry's Actor/Role.
// It is optional: the fully-trusted operator listeners (unix socket, tailnet
// TCP) carry no per-request identity, so until an identity layer fills it the
// audit middleware attributes those requests to the operator.
type AuditIdentity struct {
	Actor string // who: a token label, user, or service identity
	Role  string // the actor's role, when the auth layer assigns one
}

type auditIdentityKey struct{}

// WithAuditIdentity attaches an authenticated identity to ctx for the audit
// middleware to record. The auth layer (Lane 2) calls this once it authenticates
// a request; its middleware must sit OUTSIDE auditMiddleware (authenticate →
// audit → handle) so the value is present when the record is written. See
// Server.Handler for the composition order.
func WithAuditIdentity(ctx context.Context, id AuditIdentity) context.Context {
	return context.WithValue(ctx, auditIdentityKey{}, id)
}

// AuditIdentityFromContext returns the identity attached by WithAuditIdentity.
func AuditIdentityFromContext(ctx context.Context) (AuditIdentity, bool) {
	id, ok := ctx.Value(auditIdentityKey{}).(AuditIdentity)
	return id, ok
}

// --- request-logging middleware -------------------------------------------

// auditMiddleware records each state-changing API request (and, with
// AuditOptions.Reads, reads too) to the ledger after it completes, capturing the
// response status. It is a no-op when the operational audit log is off, and it
// only wraps the ResponseWriter for requests it will actually record — so
// websocket/SSE paths (which it excludes anyway) are never disturbed.
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auditEnabled || !s.shouldAudit(r) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.recordRequest(r, rec.status)
	})
}

// shouldAudit decides whether a request is worth a persistent record. Reads are
// excluded unless opted in; a small set of endpoints is always excluded to keep
// the log governance-relevant rather than a firehose: the liveness/metrics polls,
// the audit read itself (so a polling viewer can't inflate the log it's reading),
// high-frequency agent-shim telemetry, and websocket attaches.
func (s *Server) shouldAudit(r *http.Request) bool {
	if isReadMethod(r.Method) && !s.auditReads {
		return false
	}
	p := r.URL.Path
	switch {
	case p == "/v1/healthz", p == "/metrics":
		return false
	case strings.HasPrefix(p, "/v1/audit"), strings.HasPrefix(p, "/v1/activity"):
		// The audit read and its team-facing activity-feed sibling: excluded so a
		// polling reader can't inflate the very log it's reading.
		return false
	case p == "/v1/internal/agent-event":
		return false
	case strings.HasSuffix(p, "/session"):
		return false
	}
	return true
}

func isReadMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// recordRequest appends one api.request entry. Best-effort: the request already
// completed, so a ledger failure must never affect the response (it's logged
// inside ledgerAppend).
func (s *Server) recordRequest(r *http.Request, status int) {
	actor, role := s.resolveActor(r)
	_ = s.ledgerAppend(ledger.Entry{
		Type:     auditTypeAPIRequest,
		Island:   auditIslandFromPath(r.URL.Path),
		Method:   r.Method,
		Path:     r.URL.Path,
		Status:   status,
		Actor:    actor,
		Role:     role,
		Decision: decisionForStatus(status),
	})
}

// resolveActor attributes a request to a caller. An explicit identity from the
// auth layer (Lane 2) wins; failing that, a token-listener request is attributed
// to its island; otherwise the request arrived on a fully-trusted operator
// listener and is attributed to the operator.
func (s *Server) resolveActor(r *http.Request) (actor, role string) {
	if id, ok := AuditIdentityFromContext(r.Context()); ok && id.Actor != "" {
		return id.Actor, id.Role
	}
	if isl := TokenIslandFromContext(r.Context()); isl != "" {
		return "island:" + isl, "agent"
	}
	return "operator", "operator"
}

// decisionForStatus maps an HTTP status to the ledger's allowed/denied vocabulary
// so denied attempts (auth failures, forbidden routes, bad requests) are easy to
// filter for — the security-relevant slice of the operational log.
func decisionForStatus(status int) string {
	if status >= 400 {
		return "denied"
	}
	return "allowed"
}

// auditIslandFromPath best-effort extracts the {name} segment from an
// island-scoped path (/v1/islands/{name}/...) for display attribution. Unlike
// tokenauth's islandFromPath (a security decision), this is informational, so it
// stays simple and returns "" when there is no island segment.
func auditIslandFromPath(p string) string {
	const prefix = "/v1/islands/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := p[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// statusRecorder captures the response status for the audit record while passing
// every write straight through. It forwards Flush/Hijack so a streaming or
// upgrading handler still works if one is ever recorded.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("audit: underlying ResponseWriter is not a Hijacker")
}

// --- lifecycle events ------------------------------------------------------

// auditableLifecycle is the governance-relevant subset of the event catalog
// recorded to the ledger when the operational audit log is on. It deliberately
// omits high-frequency telemetry (agent waiting/complete/error, island.running)
// and presence (client attach/detach — surveillance-free by design; the
// in-memory ClientHistory ring already covers that ephemerally).
var auditableLifecycle = map[events.Type]bool{
	events.TypeIslandCreated:      true,
	events.TypeIslandHibernated:   true,
	events.TypeIslandWoken:        true,
	events.TypeIslandReset:        true,
	events.TypeIslandUpgraded:     true,
	events.TypeIslandPurged:       true,
	events.TypeIslandAgentAdded:   true,
	events.TypeIslandAgentRemoved: true,
	events.TypePanicEngaged:       true,
	events.TypePanicCleared:       true,
	events.TypeDaemonStarted:      true,
	events.TypeContainerCrashed:   true,
	events.TypeAgentSilent:        true, // operator alert: a running agent went silent
	events.TypeAgentRecovered:     true, // its heartbeat came back
}

// auditLifecycle appends a governance-relevant lifecycle event to the ledger.
// Called from emit(); a no-op unless the operational audit log is enabled and
// the event type is in auditableLifecycle.
func (s *Server) auditLifecycle(e events.Event) {
	if !s.auditEnabled || !auditableLifecycle[e.Type] {
		return
	}
	_ = s.ledgerAppend(ledger.Entry{
		Type:       string(e.Type),
		Island:     e.Island,
		Agent:      e.Agent, // id stays the lookup handle
		AgentLabel: eventAgentLabel(e),
		Detail:     lifecycleDetail(e),
	})
}

// eventAgentLabel pulls the agent's human-given label out of an event payload,
// when the emitter included one (the agent-added/removed lifecycle events do).
// It lets the ledger carry a name alongside the bare agent id for read-side
// display, without the id (the keyed lookup handle) ever changing.
func eventAgentLabel(e events.Event) string {
	if e.Payload == nil {
		return ""
	}
	if v, ok := e.Payload["label"]; ok {
		if sv, ok := v.(string); ok {
			return sv
		}
	}
	return ""
}

// lifecycleDetail extracts a short human-readable note from an event's payload
// for the audit record (e.g. the added agent's type, a crash reason). Best-effort
// and compact — the full payload lives in the webhook stream, not the ledger.
func lifecycleDetail(e events.Event) string {
	if e.Payload == nil {
		return ""
	}
	for _, k := range []string{"type", "reason", "agent_type"} {
		if v, ok := e.Payload[k]; ok {
			if sv, ok := v.(string); ok && sv != "" {
				return k + "=" + sv
			}
		}
	}
	return ""
}

// --- read / filter / export API -------------------------------------------

// RegisterAudit registers the audit read/filter/export/verify route on mux. It's
// the single seam server.go wires in (one line), keeping the route table change
// per-lane to a one-liner.
func (s *Server) RegisterAudit(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/audit", s.handleAudit)
}

// handleAudit reads, filters, and (optionally) exports the ledger, and reports
// the result of verifying its hash chain. Verification ALWAYS runs over the full
// chain — filters and limits only narrow what is returned, never what is
// verified. Filters (all optional, AND-combined): island, type (prefix, e.g.
// "port" matches port.grant/port.revoke), actor, decision (allowed|denied),
// since/until (RFC3339), limit (last N after filtering). format=json (default) |
// jsonl | csv selects the representation; jsonl/csv stream a download.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	lg, err := ledger.Default()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entries, err := lg.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	total := len(entries)
	verified := true
	var verr error
	if verr = lg.Verify(); verr != nil {
		verified = false
	}

	f, ferr := parseAuditFilter(r.URL.Query())
	if ferr != nil {
		writeError(w, http.StatusBadRequest, ferr)
		return
	}
	out := f.apply(entries)
	if f.limit > 0 && f.limit < len(out) {
		out = out[len(out)-f.limit:]
	}

	switch f.format {
	case "jsonl":
		writeAuditJSONL(w, out)
		return
	case "csv":
		writeAuditCSV(w, out)
		return
	}
	resp := AuditResponse{Entries: out, Total: total, Returned: len(out), Verified: verified}
	if verr != nil {
		resp.Error = verr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// auditFilter is the parsed query for GET /v1/audit.
type auditFilter struct {
	limit      int
	since      time.Time
	until      time.Time
	island     string
	typePrefix string
	actor      string
	decision   string
	format     string // "" (=json) | jsonl | csv
}

func parseAuditFilter(q map[string][]string) (auditFilter, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	f := auditFilter{
		island:     get("island"),
		typePrefix: get("type"),
		actor:      get("actor"),
		decision:   get("decision"),
		format:     strings.ToLower(get("format")),
	}
	if n := get("limit"); n != "" {
		lim, err := strconv.Atoi(n)
		if err != nil || lim < 0 {
			return f, fmt.Errorf("invalid limit %q (want a non-negative integer)", n)
		}
		f.limit = lim
	}
	if s := get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return f, fmt.Errorf("invalid since %q (want RFC3339, e.g. 2026-06-20T00:00:00Z)", s)
		}
		f.since = t
	}
	if s := get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return f, fmt.Errorf("invalid until %q (want RFC3339, e.g. 2026-06-20T00:00:00Z)", s)
		}
		f.until = t
	}
	switch f.decision {
	case "", "allowed", "denied":
	default:
		return f, fmt.Errorf("invalid decision %q (want allowed or denied)", f.decision)
	}
	switch f.format {
	case "", "json", "jsonl", "csv":
		if f.format == "json" {
			f.format = ""
		}
	default:
		return f, fmt.Errorf("invalid format %q (want json, jsonl, or csv)", f.format)
	}
	return f, nil
}

// apply returns the entries matching the filter, in order. A nil/zero filter
// returns every entry. The returned slice never aliases the input's backing
// array (it starts from a fresh allocation).
func (f auditFilter) apply(entries []ledger.Entry) []ledger.Entry {
	out := make([]ledger.Entry, 0, len(entries))
	for _, e := range entries {
		if f.island != "" && e.Island != f.island {
			continue
		}
		if f.typePrefix != "" && !typeMatches(e.Type, f.typePrefix) {
			continue
		}
		if f.actor != "" && e.Actor != f.actor {
			continue
		}
		if f.decision != "" && e.Decision != f.decision {
			continue
		}
		if !f.since.IsZero() && e.Time.Before(f.since) {
			continue
		}
		if !f.until.IsZero() && e.Time.After(f.until) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// typeMatches reports whether an entry's type matches the filter. A bare prefix
// matches the dotted namespace (e.g. "port" matches "port.grant" but not
// "portfolio.x"); an exact dotted type matches exactly.
func typeMatches(typ, filter string) bool {
	if typ == filter {
		return true
	}
	return strings.HasPrefix(typ, filter+".")
}

func writeAuditJSONL(w http.ResponseWriter, entries []ledger.Entry) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="dejima-audit.jsonl"`)
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, e := range entries {
		_ = enc.Encode(e) // Encode appends a newline → one JSON object per line
	}
}

func writeAuditCSV(w http.ResponseWriter, entries []ledger.Entry) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="dejima-audit.csv"`)
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"seq", "time", "type", "island", "agent", "agent_label", "actor", "role",
		"method", "path", "status", "scope", "mode", "bytes", "decision", "detail", "chain",
	})
	for _, e := range entries {
		_ = cw.Write([]string{
			strconv.FormatUint(e.Seq, 10),
			e.Time.UTC().Format(time.RFC3339),
			e.Type, e.Island, e.Agent, e.AgentLabel, e.Actor, e.Role,
			e.Method, e.Path, statusStr(e.Status), e.Scope, e.Mode,
			strconv.FormatInt(e.Bytes, 10), e.Decision, e.Detail, e.Chain,
		})
	}
	cw.Flush()
}

func statusStr(code int) string {
	if code == 0 {
		return ""
	}
	return strconv.Itoa(code)
}
