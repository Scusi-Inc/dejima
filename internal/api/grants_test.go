package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestUnifiedGrants_DenyAllOnFreshIsland is the deny-all proof: a freshly
// created island holds zero grants of every type, and the unified view returns
// all four as empty (non-null) arrays — no convenience default (e.g. mount-$HOME)
// leaks in anywhere. It then grants one of each kind and asserts the aggregate
// reflects them, including the link filter (only channels touching this island).
func TestUnifiedGrants_DenyAllOnFreshIsland(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	// A fresh island: every grant type empty across the unified view.
	rr := do(t, h, http.MethodGet, "/v1/islands/oc/grants", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("grants: %d %s", rr.Code, rr.Body.String())
	}
	var g IslandGrantsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode grants: %v (%s)", err, rr.Body.String())
	}
	if len(g.Port) != 0 || len(g.Capability) != 0 || len(g.MCP) != 0 || len(g.Links) != 0 {
		t.Fatalf("fresh island is not deny-all: %+v", g)
	}
	// Arrays must serialize as [] (not null) so the consumer never sees a null.
	if got := rr.Body.String(); !jsonHasEmptyArrays(got) {
		t.Errorf("deny-all view should serialize empty arrays, got %s", got)
	}

	// Cross-check: each per-resource list endpoint also reports empty — the
	// unified view and the four it aggregates agree on deny-all.
	assertEmptyList(t, h, "/v1/islands/oc/port/scopes", "scopes")
	assertEmptyList(t, h, "/v1/islands/oc/capability/grants", "grants")
	assertEmptyList(t, h, "/v1/islands/oc/mcp/grants", "grants")

	// Now grant one of each kind, then confirm the unified view aggregates them.
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/capability/grants", `{"target":"MorningBrief"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant capability: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant mcp: %d %s", rr.Code, rr.Body.String())
	}
	// A link grant needs a second island to point at, and is filtered to channels
	// touching this island.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"peer","agent":"openclaw","role":"home"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create peer island: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/links", `{"from":"oc","to":"peer","topic":"news"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant link: %d %s", rr.Code, rr.Body.String())
	}
	// And one link that does NOT touch oc — it must be filtered out of oc's view.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"other","agent":"openclaw","role":"home"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create other island: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/links", `{"from":"peer","to":"other","topic":"side"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant unrelated link: %d %s", rr.Code, rr.Body.String())
	}

	rr = do(t, h, http.MethodGet, "/v1/islands/oc/grants", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("grants after grants: %d %s", rr.Code, rr.Body.String())
	}
	g = IslandGrantsResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode grants: %v (%s)", err, rr.Body.String())
	}
	if len(g.Capability) != 1 || g.Capability[0].Target != "MorningBrief" {
		t.Errorf("capability = %+v, want one MorningBrief", g.Capability)
	}
	if len(g.MCP) != 1 || g.MCP[0].Server != "files" {
		t.Errorf("mcp = %+v, want one files", g.MCP)
	}
	if len(g.Port) != 0 {
		t.Errorf("port = %+v, want empty (none granted)", g.Port)
	}
	// Only the oc↔peer channel is returned; the peer→other channel is filtered.
	if len(g.Links) != 1 || g.Links[0].From != "oc" || g.Links[0].To != "peer" {
		t.Errorf("links = %+v, want only the oc->peer channel", g.Links)
	}
}

// jsonHasEmptyArrays asserts the deny-all body carries each field as [] not null.
func jsonHasEmptyArrays(body string) bool {
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &raw) != nil {
		return false
	}
	for _, k := range []string{"port", "capability", "mcp", "links"} {
		if string(raw[k]) != "[]" {
			return false
		}
	}
	return true
}

// assertEmptyList checks a per-resource list endpoint returns an empty slice
// under the named JSON field — the deny-all cross-check against the unified view.
func assertEmptyList(t *testing.T, h http.Handler, path, field string) {
	t.Helper()
	rr := do(t, h, http.MethodGet, path, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("%s decode: %v", path, err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw[field], &arr); err != nil {
		t.Fatalf("%s field %q not an array: %v", path, field, err)
	}
	if len(arr) != 0 {
		t.Errorf("%s field %q = %v, want empty (deny-all)", path, field, arr)
	}
}
