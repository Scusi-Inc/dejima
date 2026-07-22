package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/ledger"
)

// newAuditServer returns a Server with an isolated, fresh ledger (HOME→temp,
// Default cache reset) so each test starts with an empty chain and doesn't leak
// into others.
func newAuditServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	t.Cleanup(ledger.ResetDefault)
	return NewServer(&fakeRuntime{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func appendN(t *testing.T, entries ...ledger.Entry) {
	t.Helper()
	lg, err := ledger.Default()
	if err != nil {
		t.Fatalf("default ledger: %v", err)
	}
	for _, e := range entries {
		if _, err := lg.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func readLedger(t *testing.T) []ledger.Entry {
	t.Helper()
	lg, err := ledger.Default()
	if err != nil {
		t.Fatalf("default ledger: %v", err)
	}
	es, err := lg.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return es
}

func TestParseAuditFilter(t *testing.T) {
	f, err := parseAuditFilter(map[string][]string{
		"island":   {"x"},
		"type":     {"port"},
		"actor":    {"operator"},
		"decision": {"denied"},
		"limit":    {"10"},
		"since":    {"2026-06-20T00:00:00Z"},
		"until":    {"2026-06-21T00:00:00Z"},
		"format":   {"csv"},
	})
	if err != nil {
		t.Fatalf("valid filter: %v", err)
	}
	if f.island != "x" || f.typePrefix != "port" || f.actor != "operator" ||
		f.decision != "denied" || f.limit != 10 || f.format != "csv" || f.since.IsZero() || f.until.IsZero() {
		t.Fatalf("parsed wrong: %+v", f)
	}

	for name, q := range map[string]map[string][]string{
		"bad limit":    {"limit": {"-1"}},
		"bad since":    {"since": {"nope"}},
		"bad decision": {"decision": {"maybe"}},
		"bad format":   {"format": {"xml"}},
	} {
		if _, err := parseAuditFilter(q); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	// format=json normalizes to "" (the default JSON response path).
	f, _ = parseAuditFilter(map[string][]string{"format": {"json"}})
	if f.format != "" {
		t.Fatalf("format json should normalize to empty, got %q", f.format)
	}
}

func TestAuditFilterApply(t *testing.T) {
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	entries := []ledger.Entry{
		{Seq: 1, Time: base, Type: "port.grant", Island: "a", Actor: "operator", Decision: "allowed"},
		{Seq: 2, Time: base.Add(time.Hour), Type: "api.request", Island: "b", Actor: "operator", Decision: "denied"},
		{Seq: 3, Time: base.Add(2 * time.Hour), Type: "island.created", Island: "a", Actor: "operator", Decision: "allowed"},
		{Seq: 4, Time: base.Add(3 * time.Hour), Type: "port.revoke", Island: "a", Actor: "alice", Decision: "allowed"},
	}

	got := auditFilter{island: "a"}.apply(entries)
	if len(got) != 3 {
		t.Fatalf("island filter: got %d", len(got))
	}
	got = auditFilter{typePrefix: "port"}.apply(entries)
	if len(got) != 2 {
		t.Fatalf("type prefix: got %d", len(got))
	}
	got = auditFilter{decision: "denied"}.apply(entries)
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("decision filter: %+v", got)
	}
	got = auditFilter{actor: "alice"}.apply(entries)
	if len(got) != 1 || got[0].Seq != 4 {
		t.Fatalf("actor filter: %+v", got)
	}
	got = auditFilter{since: base.Add(90 * time.Minute), until: base.Add(150 * time.Minute)}.apply(entries)
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("time window: %+v", got)
	}
	// apply never aliases the input backing array.
	got = auditFilter{}.apply(entries)
	if len(got) != 4 {
		t.Fatalf("empty filter should pass all: %d", len(got))
	}
}

func TestTypeMatches(t *testing.T) {
	cases := []struct {
		typ, filter string
		want        bool
	}{
		{"port.grant", "port", true},
		{"port.grant", "port.grant", true},
		{"portfolio.x", "port", false}, // prefix matches the dotted namespace only
		{"api.request", "api", true},
		{"island.created", "island", true},
		{"trade.read", "port", false},
	}
	for _, c := range cases {
		if got := typeMatches(c.typ, c.filter); got != c.want {
			t.Errorf("typeMatches(%q,%q)=%v want %v", c.typ, c.filter, got, c.want)
		}
	}
}

func TestDecisionForStatus(t *testing.T) {
	if decisionForStatus(200) != "allowed" || decisionForStatus(201) != "allowed" {
		t.Fatal("2xx should be allowed")
	}
	if decisionForStatus(403) != "denied" || decisionForStatus(500) != "denied" {
		t.Fatal("4xx/5xx should be denied")
	}
}

func TestAuditIslandFromPath(t *testing.T) {
	cases := map[string]string{
		"/v1/islands/myrepo":             "myrepo",
		"/v1/islands/myrepo/port/scopes": "myrepo",
		"/v1/islands/myrepo/agents/a2":   "myrepo",
		"/v1/overview":                   "",
		"/v1/audit":                      "",
		"/v1/islands":                    "",
	}
	for p, want := range cases {
		if got := auditIslandFromPath(p); got != want {
			t.Errorf("auditIslandFromPath(%q)=%q want %q", p, got, want)
		}
	}
}

func TestHandleAuditFilterAndFormats(t *testing.T) {
	s := newAuditServer(t)
	appendN(t,
		ledger.Entry{Type: "port.grant", Island: "a", Scope: "v", Decision: "allowed"},
		ledger.Entry{Type: "api.request", Island: "b", Method: "DELETE", Path: "/v1/islands/b", Status: 403, Actor: "operator", Decision: "denied"},
		ledger.Entry{Type: "island.created", Island: "a", Decision: "allowed"},
	)
	h := s.Handler()

	// JSON, filtered to denied api.request.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/audit?type=api&decision=denied", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var resp AuditResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 || resp.Returned != 1 || len(resp.Entries) != 1 || !resp.Verified {
		t.Fatalf("unexpected resp: total=%d returned=%d n=%d verified=%v", resp.Total, resp.Returned, len(resp.Entries), resp.Verified)
	}
	if resp.Entries[0].Type != "api.request" {
		t.Fatalf("wrong entry: %+v", resp.Entries[0])
	}

	// CSV export — header + one row per matching entry.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/audit?island=a&format=csv", nil))
	if ct := rr.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("csv content-type %q", ct)
	}
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(rows) != 3 { // header + 2 island=a entries
		t.Fatalf("csv rows: %d", len(rows))
	}
	if rows[0][0] != "seq" {
		t.Fatalf("csv header: %v", rows[0])
	}

	// JSONL export — one JSON object per line.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/audit?format=jsonl", nil))
	if ct := rr.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("jsonl content-type %q", ct)
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl lines: %d", len(lines))
	}
}

func TestAuditMiddlewareRecording(t *testing.T) {
	s := newAuditServer(t)
	s.EnableAudit(AuditOptions{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/islands/x" && r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := s.auditMiddleware(next)

	exercise := func(method, path string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
	}
	exercise(http.MethodPost, "/v1/islands")              // recorded
	exercise(http.MethodDelete, "/v1/islands/x")          // recorded (denied)
	exercise(http.MethodGet, "/v1/islands")               // NOT recorded (read, default)
	exercise(http.MethodGet, "/v1/healthz")               // NOT recorded (excluded)
	exercise(http.MethodPost, "/v1/internal/agent-event") // NOT recorded (excluded)
	exercise(http.MethodGet, "/v1/audit")                 // NOT recorded (excluded)

	es := readLedger(t)
	if len(es) != 2 {
		t.Fatalf("expected 2 recorded requests, got %d: %+v", len(es), es)
	}
	for _, e := range es {
		if e.Type != auditTypeAPIRequest || e.Actor != "operator" {
			t.Fatalf("unexpected entry: %+v", e)
		}
	}
	if es[0].Method != "POST" || es[0].Status != 200 || es[0].Decision != "allowed" {
		t.Fatalf("first entry wrong: %+v", es[0])
	}
	if es[1].Method != "DELETE" || es[1].Status != 403 || es[1].Decision != "denied" || es[1].Island != "x" {
		t.Fatalf("second entry wrong: %+v", es[1])
	}
}

func TestAuditMiddlewareDisabledAndReads(t *testing.T) {
	// Disabled: nothing recorded.
	s := newAuditServer(t)
	h := s.auditMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/islands", nil))
	if es := readLedger(t); len(es) != 0 {
		t.Fatalf("disabled audit recorded %d", len(es))
	}

	// Reads opt-in: a GET is recorded.
	s2 := newAuditServer(t)
	s2.EnableAudit(AuditOptions{Reads: true})
	h2 := s2.auditMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/islands", nil))
	es := readLedger(t)
	if len(es) != 1 || es[0].Method != "GET" {
		t.Fatalf("reads-enabled GET not recorded: %+v", es)
	}
}

func TestAuditLifecycleGating(t *testing.T) {
	s := newAuditServer(t)
	s.EnableAudit(AuditOptions{})

	s.emit(events.Event{Type: events.TypeIslandCreated, Island: "x"})
	s.emit(events.Event{Type: events.TypeIslandAgentAdded, Island: "x", Agent: "a2", Payload: map[string]any{"type": "claude-code"}})
	s.emit(events.Event{Type: events.TypeIslandRunning, Island: "x"})        // not auditable
	s.emit(events.Event{Type: events.TypeAgentWaitingForInput, Island: "x"}) // not auditable
	s.emit(events.Event{Type: events.TypeClientAttached, Island: "x"})       // not auditable (presence)

	es := readLedger(t)
	if len(es) != 2 {
		t.Fatalf("expected 2 lifecycle records, got %d: %+v", len(es), es)
	}
	if es[0].Type != string(events.TypeIslandCreated) {
		t.Fatalf("first: %+v", es[0])
	}
	if es[1].Type != string(events.TypeIslandAgentAdded) || es[1].Agent != "a2" || es[1].Detail != "type=claude-code" {
		t.Fatalf("second: %+v", es[1])
	}
}

func TestAuditLifecycleDisabled(t *testing.T) {
	s := newAuditServer(t) // audit off
	s.emit(events.Event{Type: events.TypeIslandCreated, Island: "x"})
	if es := readLedger(t); len(es) != 0 {
		t.Fatalf("disabled audit recorded lifecycle: %d", len(es))
	}
}

func TestResolveActorIdentitySeam(t *testing.T) {
	s := newAuditServer(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/islands", nil)

	// Default (trusted operator listener, no identity): operator.
	if a, role := s.resolveActor(r); a != "operator" || role != "operator" {
		t.Fatalf("default actor: %q/%q", a, role)
	}
	// Lane 2 identity wins.
	r2 := r.WithContext(WithAuditIdentity(r.Context(), AuditIdentity{Actor: "alice", Role: "operator"}))
	if a, role := s.resolveActor(r2); a != "alice" || role != "operator" {
		t.Fatalf("identity actor: %q/%q", a, role)
	}
	// Token island (when the middleware is wired into the token path) attributes
	// to the island. tokenIslandKey is what tokenauth's listener sets.
	r3 := r.WithContext(context.WithValue(r.Context(), tokenIslandKey{}, "isl1"))
	if a, role := s.resolveActor(r3); a != "island:isl1" || role != "agent" {
		t.Fatalf("token actor: %q/%q", a, role)
	}
}

func TestAuditQueryValues(t *testing.T) {
	q := AuditQuery{Limit: 5, Island: "x", Type: "port", Actor: "alice", Decision: "denied", Since: "2026-06-20T00:00:00Z", Until: "2026-06-21T00:00:00Z"}
	v := q.values()
	want := map[string]string{
		"limit": "5", "island": "x", "type": "port", "actor": "alice",
		"decision": "denied", "since": "2026-06-20T00:00:00Z", "until": "2026-06-21T00:00:00Z",
	}
	for k, w := range want {
		if got := v.Get(k); got != w {
			t.Errorf("values()[%q]=%q want %q", k, got, w)
		}
	}
	// The zero query renders no parameters (reads the whole ledger).
	if len(AuditQuery{}.values()) != 0 {
		t.Fatalf("zero query should produce no params")
	}
	// Limit 0 is omitted (0 = all), not sent as limit=0.
	if (AuditQuery{Island: "x"}).values().Has("limit") {
		t.Fatalf("limit 0 should be omitted")
	}
}
