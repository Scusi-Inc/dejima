package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/authtoken"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

func TestActivityOf(t *testing.T) {
	cases := []struct {
		name    string
		e       ledger.Entry
		keep    bool
		kind    string
		actor   string
		summary string
	}{
		{
			name: "create island (api.request)",
			e:    ledger.Entry{Type: "api.request", Method: "POST", Path: "/v1/islands", Actor: "alice", Role: "operator", Decision: "allowed"},
			keep: true, kind: kindLifecycle, actor: "alice", summary: "created an island",
		},
		{
			name: "denied purge surfaces as an attempt",
			e:    ledger.Entry{Type: "api.request", Method: "DELETE", Path: "/v1/islands/foo", Island: "foo", Actor: "phone", Role: "viewer", Decision: "denied"},
			keep: true, kind: kindLifecycle, actor: "phone", summary: "was denied: purged island foo",
		},
		{
			name: "token issuance",
			e:    ledger.Entry{Type: "api.request", Method: "POST", Path: "/v1/tokens", Actor: "alice", Role: "owner", Decision: "allowed"},
			keep: true, kind: kindLifecycle, actor: "alice", summary: "issued an API token",
		},
		{
			name: "GET request is dropped (not activity)",
			e:    ledger.Entry{Type: "api.request", Method: "GET", Path: "/v1/islands", Actor: "alice"},
			keep: false,
		},
		{
			name: "api.request on a brokered path is dropped (deduped vs broker record)",
			e:    ledger.Entry{Type: "api.request", Method: "POST", Path: "/v1/islands/foo/port/scopes", Island: "foo", Actor: "alice"},
			keep: false,
		},
		{
			name: "api.request for mcp call is dropped (broker record wins)",
			e:    ledger.Entry{Type: "api.request", Method: "POST", Path: "/v1/mcp/call", Actor: "island:foo"},
			keep: false,
		},
		{
			name: "port grant (broker, operator)",
			e:    ledger.Entry{Type: "port.grant", Island: "foo", Scope: "vault", Mode: "ro", Decision: "allowed"},
			keep: true, kind: kindBroker, actor: "operator", summary: `granted Port scope "vault" on foo (read-only)`,
		},
		{
			name: "capability execute (broker, agent)",
			e:    ledger.Entry{Type: "capability.execute", Island: "foo", Scope: "send-imessage", Decision: "allowed"},
			keep: true, kind: kindBroker, actor: "island:foo", summary: `the agent ran capability "send-imessage" on foo`,
		},
		{
			name: "mcp call denied (broker, agent)",
			e:    ledger.Entry{Type: "mcp.call", Island: "bar", Scope: "github", Decision: "denied"},
			keep: true, kind: kindBroker, actor: "island:bar", summary: `the agent called MCP server "github" on bar`,
		},
		{
			name: "trade write (broker, agent)",
			e:    ledger.Entry{Type: "trade.write", Island: "bar", Scope: "out", Path: "report.md"},
			keep: true, kind: kindBroker, actor: "island:bar", summary: `the agent wrote "report.md" via Port (out) on bar`,
		},
		{
			name: "container crash (system)",
			e:    ledger.Entry{Type: "container.crashed", Island: "foo", Detail: "oom_killed"},
			keep: true, kind: kindSystem, actor: "system", summary: "island foo crashed (oom_killed)",
		},
		{
			name: "daemon started (system)",
			e:    ledger.Entry{Type: "daemon.started"},
			keep: true, kind: kindSystem, actor: "system", summary: "the daemon started",
		},
		{
			name: "redundant lifecycle record dropped (api.request carries the actor)",
			e:    ledger.Entry{Type: "island.created", Island: "foo"},
			keep: false,
		},
		{
			name: "telemetry dropped",
			e:    ledger.Entry{Type: "agent.waiting-for-input", Island: "foo"},
			keep: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := activityOf(c.e)
			if ok != c.keep {
				t.Fatalf("keep=%v want %v", ok, c.keep)
			}
			if !c.keep {
				return
			}
			if got.Kind != c.kind {
				t.Errorf("kind=%q want %q", got.Kind, c.kind)
			}
			if got.Actor != c.actor {
				t.Errorf("actor=%q want %q", got.Actor, c.actor)
			}
			if got.Summary != c.summary {
				t.Errorf("summary=%q want %q", got.Summary, c.summary)
			}
		})
	}
}

func TestBuildActivityEnrichesOwner(t *testing.T) {
	entries := []ledger.Entry{
		{Seq: 1, Type: "api.request", Method: "POST", Path: "/v1/islands/foo/wake", Island: "foo", Actor: "alice"},
		{Seq: 2, Type: "port.grant", Island: "foo", Scope: "v"},
		{Seq: 3, Type: "daemon.started"}, // no island → no owner
	}
	owners := map[string]string{"foo": "team-web"}
	items := buildActivity(entries, owners)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	for _, it := range items {
		if it.Island == "foo" && it.Owner != "team-web" {
			t.Errorf("island foo item not enriched with owner: %+v", it)
		}
		if it.Island == "" && it.Owner != "" {
			t.Errorf("ownerless item got an owner: %+v", it)
		}
	}
}

func TestActivityFilter(t *testing.T) {
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	items := []ActivityItem{
		{Seq: 1, Time: base, Actor: "alice", Island: "foo", Owner: "web", Kind: kindLifecycle, Decision: "allowed"},
		{Seq: 2, Time: base.Add(time.Hour), Actor: "phone", Island: "foo", Owner: "web", Kind: kindLifecycle, Decision: "denied"},
		{Seq: 3, Time: base.Add(2 * time.Hour), Actor: "island:bar", Island: "bar", Owner: "api", Kind: kindBroker, Decision: "allowed"},
	}
	check := func(name string, f activityFilter, wantSeqs ...uint64) {
		t.Run(name, func(t *testing.T) {
			got := f.apply(items)
			if len(got) != len(wantSeqs) {
				t.Fatalf("got %d items, want %d", len(got), len(wantSeqs))
			}
			for i, s := range wantSeqs {
				if got[i].Seq != s {
					t.Errorf("item %d seq=%d want %d", i, got[i].Seq, s)
				}
			}
		})
	}
	check("by actor", activityFilter{actor: "alice"}, 1)
	check("by island", activityFilter{island: "foo"}, 1, 2)
	check("by owner", activityFilter{owner: "api"}, 3)
	check("by kind", activityFilter{kind: kindBroker}, 3)
	check("denied only", activityFilter{decision: "denied"}, 2)
	check("since", activityFilter{since: base.Add(90 * time.Minute)}, 3)
	check("until", activityFilter{until: base.Add(30 * time.Minute)}, 1)
}

func TestParseActivityFilterRejectsBadInput(t *testing.T) {
	for _, q := range []map[string][]string{
		{"kind": {"bogus"}},
		{"decision": {"maybe"}},
		{"limit": {"-1"}},
		{"since": {"not-a-time"}},
	} {
		if _, err := parseActivityFilter(q); err == nil {
			t.Errorf("parseActivityFilter(%v) accepted bad input", q)
		}
	}
}

// TestHandleActivity drives the endpoint end-to-end against a seeded on-disk
// ledger: newest-first ordering, owner enrichment, limit, and the AuditEnabled
// flag all come together.
func TestHandleActivity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	t.Cleanup(ledger.ResetDefault)

	// Owner enrichment source.
	p := &project.Project{Name: "foo", Owner: "team-web", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatalf("save project: %v", err)
	}

	lg, err := ledger.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []ledger.Entry{
		{Type: "api.request", Method: "POST", Path: "/v1/islands", Actor: "alice", Role: "operator", Decision: "allowed"},
		{Type: "port.grant", Island: "foo", Scope: "vault", Mode: "ro", Decision: "allowed"},
		{Type: "api.request", Method: "GET", Path: "/v1/islands", Actor: "alice"}, // dropped
		{Type: "mcp.call", Island: "foo", Scope: "github", Decision: "denied"},
	} {
		if _, err := lg.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), auditEnabled: true}
	r := httptest.NewRequest("GET", "/v1/activity", nil)
	w := httptest.NewRecorder()
	s.handleActivity(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", w.Code)
	}
	var resp ActivityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.AuditEnabled {
		t.Error("AuditEnabled should be true")
	}
	// 3 feed items (the GET is dropped), newest-first by seq.
	if resp.Returned != 3 || len(resp.Items) != 3 {
		t.Fatalf("returned=%d items=%d want 3", resp.Returned, len(resp.Items))
	}
	if resp.Items[0].Seq < resp.Items[1].Seq || resp.Items[1].Seq < resp.Items[2].Seq {
		t.Errorf("items not newest-first: %d, %d, %d", resp.Items[0].Seq, resp.Items[1].Seq, resp.Items[2].Seq)
	}
	// The mcp.call (newest) is a denied broker item on foo, owner-enriched.
	top := resp.Items[0]
	if top.Kind != kindBroker || top.Decision != "denied" || top.Owner != "team-web" {
		t.Errorf("top item = %+v, want denied broker on foo owned by team-web", top)
	}

	// A token scoped to island "bar" must not see "foo"'s activity (or the
	// account-level "created an island" item, which has no island).
	scopedReq := httptest.NewRequest("GET", "/v1/activity", nil)
	scopedReq = scopedReq.WithContext(WithIdentity(scopedReq.Context(),
		idFor(authtoken.RoleViewer, "bar")))
	ws := httptest.NewRecorder()
	s.handleActivity(ws, scopedReq)
	var scopedResp ActivityResponse
	_ = json.Unmarshal(ws.Body.Bytes(), &scopedResp)
	if scopedResp.Returned != 0 {
		t.Errorf("scoped-to-bar token saw %d items (all activity is foo/account-level); want 0", scopedResp.Returned)
	}

	// limit=1 returns only the newest.
	r2 := httptest.NewRequest("GET", "/v1/activity?limit=1", nil)
	w2 := httptest.NewRecorder()
	s.handleActivity(w2, r2)
	var resp2 ActivityResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Returned != 1 {
		t.Errorf("limit=1 returned %d", resp2.Returned)
	}
}
