package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// This file is the team activity feed — the team-facing join of Lane 1's audit
// log and Lane 2's roles/identity (build-queue #2's last item). Where GET
// /v1/audit hands back the raw, tamper-evident ledger for compliance, the
// activity feed is a *curated, human-rendered* timeline of "who launched what,
// and which agent did what," enriched with each island's owner. It is a
// read-only projection over the same ledger — it records nothing of its own.
//
// Sources, deduped so each action appears once:
//   - api.request mutations (non-GET, non-broker paths) → the operator/human
//     "who did what" (create/purge/hibernate/token/credential/panic…), carrying
//     the authenticated actor+role from the auth layer.
//   - brokered records (port.*/trade.*/capability.*/mcp.*) → "which agent did
//     what" to the host. These are always-on (independent of --audit), so the
//     feed shows agent↔host activity even when the operational log is off.
//   - container.crashed / daemon.started → system events.
// Everything else (reads, the redundant island.* lifecycle records, telemetry)
// is dropped. The api.request layer and the lifecycle records both need --audit;
// the brokered records do not — so the feed degrades gracefully to agent↔host
// activity when --audit is off (AuditEnabled flags that in the response).

// ActivityItem is one rendered entry in the team activity feed.
type ActivityItem struct {
	Seq  uint64    `json:"seq"`
	Time time.Time `json:"time"`
	// Actor is who acted: a token label / "operator" / "island:<name>" (an agent).
	Actor string `json:"actor"`
	Role  string `json:"role,omitempty"`
	// Island is the island acted on (or the acting agent's island), when any.
	Island string `json:"island,omitempty"`
	// Owner is that island's owner label, enriched from project config.
	Owner string `json:"owner,omitempty"`
	// Kind buckets the item for filtering/rendering: "lifecycle" (operator island
	// + account ops), "broker" (Port/capability/MCP host access), or "system".
	Kind string `json:"kind"`
	// Summary is the human-readable one-liner ("alice created an island").
	Summary string `json:"summary"`
	// Decision is "allowed" | "denied" (denied attempts are the security-relevant
	// slice — a viewer refused a purge, an agent refused an ungranted MCP server).
	Decision string `json:"decision,omitempty"`
}

// ActivityResponse is the body of GET /v1/activity.
type ActivityResponse struct {
	Items    []ActivityItem `json:"items"`
	Returned int            `json:"returned"`
	// AuditEnabled reflects whether the operational audit log is on. When false,
	// the feed carries only the always-on brokered (agent↔host) records — a hint
	// for clients to suggest enabling --audit for the full who-did-what timeline.
	AuditEnabled bool `json:"audit_enabled"`
}

// activity kinds.
const (
	kindLifecycle = "lifecycle"
	kindBroker    = "broker"
	kindSystem    = "system"
)

// RegisterActivity registers the team activity-feed route (one append-only line
// in routes(), per the lane seam contract). Read-only and operator-surface only;
// classified capRead in roleRouteCap so viewers can observe team activity, and
// absent from tokenRouteAccess so an island token can never read the fleet feed.
func (s *Server) RegisterActivity(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/activity", s.handleActivity)
}

// handleActivity renders the curated activity feed from the ledger, newest-first,
// enriched with island owners and narrowed by the query filters.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
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
	f, err := parseActivityFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	items := buildActivity(entries, s.islandOwners())
	items = f.apply(items)
	// Newest-first for a feed; the ledger is oldest-first (append order).
	sort.SliceStable(items, func(i, j int) bool { return items[i].Seq > items[j].Seq })
	if f.limit > 0 && f.limit < len(items) {
		items = items[:f.limit]
	}
	writeJSON(w, http.StatusOK, ActivityResponse{
		Items:        items,
		Returned:     len(items),
		AuditEnabled: s.auditEnabled,
	})
}

// islandOwners returns a name→owner map for ownership enrichment. Best-effort: a
// project-list failure yields an empty map (items just carry no owner).
func (s *Server) islandOwners() map[string]string {
	out := map[string]string{}
	projects, err := project.List()
	if err != nil {
		return out
	}
	for _, p := range projects {
		if p.Owner != "" {
			out[p.Name] = p.Owner
		}
	}
	return out
}

// buildActivity classifies and renders feed-worthy ledger entries, dropping the
// rest. ownerOf maps an island name to its owner label for enrichment.
func buildActivity(entries []ledger.Entry, ownerOf map[string]string) []ActivityItem {
	out := make([]ActivityItem, 0, len(entries))
	for _, e := range entries {
		item, ok := activityOf(e)
		if !ok {
			continue
		}
		if item.Island != "" {
			item.Owner = ownerOf[item.Island]
		}
		out = append(out, item)
	}
	return out
}

// activityOf maps a single ledger entry to a rendered ActivityItem, or ok=false
// to drop it. The classification is the heart of the feed — see the file comment
// for the source rules.
func activityOf(e ledger.Entry) (ActivityItem, bool) {
	base := ActivityItem{
		Seq:      e.Seq,
		Time:     e.Time,
		Island:   e.Island,
		Decision: e.Decision,
	}
	switch {
	case e.Type == auditTypeAPIRequest:
		// Reads and brokered-op requests are not feed items: reads aren't activity,
		// and brokered ops are rendered from their own (richer, always-on) records.
		if isReadMethod(e.Method) || isBrokeredPath(e.Path) {
			return ActivityItem{}, false
		}
		base.Actor, base.Role = e.Actor, e.Role
		base.Kind = kindLifecycle
		base.Summary = requestSummary(e)
		return base, true

	case strings.HasPrefix(e.Type, "port."), strings.HasPrefix(e.Type, "trade."),
		strings.HasPrefix(e.Type, "capability."), strings.HasPrefix(e.Type, "mcp."):
		base.Kind = kindBroker
		base.Actor, base.Role = brokerActor(e)
		base.Summary = brokerSummary(e)
		return base, true

	case e.Type == "container.crashed":
		base.Kind = kindSystem
		base.Actor, base.Role = "system", "system"
		base.Summary = "island " + e.Island + " crashed"
		if e.Detail != "" {
			base.Summary += " (" + e.Detail + ")"
		}
		return base, true

	case e.Type == "daemon.started":
		base.Kind = kindSystem
		base.Actor, base.Role = "system", "system"
		base.Summary = "the daemon started"
		return base, true

	default:
		// Redundant lifecycle records (island.created etc. — their api.request
		// counterpart carries the actor), telemetry, and anything unknown.
		return ActivityItem{}, false
	}
}

// isBrokeredPath reports whether an api.request path is a brokered host-access op
// (Port/capability/MCP), which the feed renders from its own always-on ledger
// record instead — so the api.request copy is dropped to avoid a duplicate.
func isBrokeredPath(path string) bool {
	return strings.Contains(path, "/port/") ||
		strings.Contains(path, "/capability/") ||
		strings.Contains(path, "/mcp/") ||
		path == "/v1/capabilities/execute"
}

// brokerActor attributes a brokered record. Grants/revokes are the operator's act
// of authorizing host access; trades/executions/calls are the in-island agent
// reaching the host. The record itself rarely carries a human actor, so this
// derives a sensible one from the record type.
func brokerActor(e ledger.Entry) (actor, role string) {
	if e.Actor != "" {
		return e.Actor, e.Role
	}
	switch {
	case strings.HasSuffix(e.Type, ".grant"), strings.HasSuffix(e.Type, ".revoke"):
		return "operator", "operator"
	default:
		if e.Island != "" {
			return "island:" + e.Island, "agent"
		}
		return "operator", "operator"
	}
}

// brokerSummary renders a brokered record into a human one-liner. Scope holds the
// target (Port scope / capability name / MCP server); Path holds the file for a
// trade.
func brokerSummary(e ledger.Entry) string {
	on := ""
	if e.Island != "" {
		on = " on " + e.Island
	}
	switch e.Type {
	case "port.grant":
		return fmt.Sprintf("granted Port scope %q%s%s", e.Scope, on, modeSuffix(e.Mode))
	case "port.revoke":
		return fmt.Sprintf("revoked Port scope %q%s", e.Scope, on)
	case "trade.read":
		return fmt.Sprintf("the agent read %q via Port (%s)%s", e.Path, e.Scope, on)
	case "trade.write":
		return fmt.Sprintf("the agent wrote %q via Port (%s)%s", e.Path, e.Scope, on)
	case "trade.export":
		return fmt.Sprintf("the agent exported %q via Port%s", e.Path, on)
	case "trade.deny":
		return fmt.Sprintf("the agent was denied a Port trade (%s)%s", e.Scope, on)
	case "capability.grant":
		return fmt.Sprintf("granted capability %q%s", e.Scope, on)
	case "capability.revoke":
		return fmt.Sprintf("revoked capability %q%s", e.Scope, on)
	case "capability.execute":
		return fmt.Sprintf("the agent ran capability %q%s", e.Scope, on)
	case "capability.deny":
		return fmt.Sprintf("the agent was denied capability %q%s", e.Scope, on)
	case "mcp.grant":
		return fmt.Sprintf("granted MCP server %q%s", e.Scope, on)
	case "mcp.revoke":
		return fmt.Sprintf("revoked MCP server %q%s", e.Scope, on)
	case "mcp.call":
		return fmt.Sprintf("the agent called MCP server %q%s", e.Scope, on)
	case "mcp.deny":
		return fmt.Sprintf("the agent was denied MCP server %q%s", e.Scope, on)
	default:
		return e.Type + on
	}
}

func modeSuffix(mode string) string {
	switch mode {
	case "ro":
		return " (read-only)"
	case "rw":
		return " (read-write)"
	default:
		return ""
	}
}

// requestSummary renders an api.request mutation into a human one-liner, using
// the method + path (the island is already on the entry). A denied request reads
// as an attempt ("was denied: …").
func requestSummary(e ledger.Entry) string {
	verb := requestVerb(e.Method, e.Path, e.Island)
	if e.Decision == "denied" {
		return "was denied: " + verb
	}
	return verb
}

// requestVerb maps a mutating request to a human verb phrase. island is the
// already-extracted island segment (may be ""). Falls back to "<method> <path>".
func requestVerb(method, path, island string) string {
	on := ""
	if island != "" {
		on = " " + island
	}
	switch {
	case method == http.MethodPost && path == "/v1/islands":
		return "created an island"
	case method == http.MethodDelete && isIslandRoot(path):
		return "purged island" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/hibernate"):
		return "hibernated" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/wake"):
		return "woke" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/reset"):
		return "reset" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/upgrade"):
		return "upgraded" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/clone"):
		return "cloned" + on
	case method == http.MethodPut && strings.HasSuffix(path, "/resources"):
		return "changed resources on" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/exec"):
		return "ran a command in" + on
	case method == http.MethodPut && strings.Contains(path, "/files/"):
		return "wrote a file in" + on
	case method == http.MethodPost && strings.HasSuffix(path, "/agents"):
		return "added an agent to" + on
	case method == http.MethodDelete && strings.Contains(path, "/agents/"):
		return "removed an agent from" + on
	case method == http.MethodPatch && strings.HasSuffix(path, "/config"):
		return "configured an agent on" + on
	case method == http.MethodPatch && strings.Contains(path, "/agents/"):
		return "relabeled an agent on" + on
	case method == http.MethodPatch && isIslandRoot(path):
		return "renamed island" + on
	case method == http.MethodPost && path == "/v1/tokens":
		return "issued an API token"
	case method == http.MethodDelete && strings.HasPrefix(path, "/v1/tokens/"):
		return "revoked an API token"
	case method == http.MethodPut && path == "/v1/credentials/claude":
		return "updated Claude credentials"
	case method == http.MethodPut && strings.HasPrefix(path, "/v1/credentials/github/"):
		return "updated a GitHub identity"
	case method == http.MethodDelete && strings.HasPrefix(path, "/v1/credentials/github/"):
		return "removed a GitHub identity"
	case method == http.MethodPut && strings.HasPrefix(path, "/v1/credentials/providers/"):
		return "updated a provider key"
	case method == http.MethodDelete && strings.HasPrefix(path, "/v1/credentials/providers/"):
		return "removed a provider key"
	case method == http.MethodPost && path == "/v1/panic":
		return "engaged the panic stop"
	case method == http.MethodDelete && path == "/v1/panic":
		return "cleared the panic stop"
	case method == http.MethodPost && path == "/v1/sessions/revoke":
		return "revoked all sessions"
	case method == http.MethodPost && path == "/v1/image/build":
		return "rebuilt the island image"
	case method == http.MethodPost && path == "/v1/admin/update":
		return "triggered a daemon self-update"
	case method == http.MethodPost && path == "/v1/ssh/account-keys":
		return "authorized an SSH key"
	case method == http.MethodPost && path == "/v1/events/subscribe":
		return "added a webhook"
	case method == http.MethodDelete && strings.HasPrefix(path, "/v1/events/subscriptions/"):
		return "removed a webhook"
	case method == http.MethodPost && strings.HasSuffix(path, "/terminals"):
		return "opened a host terminal"
	case method == http.MethodDelete && strings.Contains(path, "/terminals/"):
		return "closed a host terminal"
	default:
		return strings.ToLower(method) + " " + path
	}
}

// isIslandRoot reports whether path is exactly /v1/islands/{name} (no trailing
// sub-resource) — the purge (DELETE) and rename (PATCH) target.
func isIslandRoot(path string) bool {
	const prefix = "/v1/islands/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return !strings.Contains(path[len(prefix):], "/")
}

// activityFilter is the parsed query for GET /v1/activity. All optional, AND-combined.
type activityFilter struct {
	actor    string
	island   string
	owner    string
	kind     string
	decision string
	since    time.Time
	until    time.Time
	limit    int
}

func parseActivityFilter(q map[string][]string) (activityFilter, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	f := activityFilter{
		actor:    get("actor"),
		island:   get("island"),
		owner:    get("owner"),
		kind:     strings.ToLower(get("kind")),
		decision: strings.ToLower(get("decision")),
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
			return f, fmt.Errorf("invalid until %q (want RFC3339)", s)
		}
		f.until = t
	}
	switch f.kind {
	case "", kindLifecycle, kindBroker, kindSystem:
	default:
		return f, fmt.Errorf("invalid kind %q (want lifecycle, broker, or system)", f.kind)
	}
	switch f.decision {
	case "", "allowed", "denied":
	default:
		return f, fmt.Errorf("invalid decision %q (want allowed or denied)", f.decision)
	}
	return f, nil
}

func (f activityFilter) apply(items []ActivityItem) []ActivityItem {
	out := make([]ActivityItem, 0, len(items))
	for _, it := range items {
		if f.actor != "" && it.Actor != f.actor {
			continue
		}
		if f.island != "" && it.Island != f.island {
			continue
		}
		if f.owner != "" && it.Owner != f.owner {
			continue
		}
		if f.kind != "" && it.Kind != f.kind {
			continue
		}
		if f.decision != "" && it.Decision != f.decision {
			continue
		}
		if !f.since.IsZero() && it.Time.Before(f.since) {
			continue
		}
		if !f.until.IsZero() && it.Time.After(f.until) {
			continue
		}
		out = append(out, it)
	}
	return out
}
