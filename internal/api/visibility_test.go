package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/authtoken"
)

// doAuth drives a request through the full operator handler with a bearer token
// (so roleAuth resolves the caller's identity), unlike do() which is the trusted
// local owner.
func doAuth(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func islandNames(t *testing.T, rr *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var list []IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode island list: %v (%s)", err, rr.Body)
	}
	m := map[string]bool{}
	for _, i := range list {
		m[i.Name] = true
	}
	return m
}

// TestPrivateVisibility (P2): a teammate sees + counts only its own islands; the
// host owner sees the whole fleet; a teammate can't read another owner's island.
func TestPrivateVisibility(t *testing.T) {
	h, _ := newTestServer(t)
	amanda, _, err := authtoken.Create("amanda", authtoken.RoleOperator, nil, "amanda")
	if err != nil {
		t.Fatal(err)
	}

	// Host owner (trusted local) creates hostisle; amanda (teammate token) creates amandaisle.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"hostisle","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create hostisle: %d %s", rr.Code, rr.Body)
	}
	if rr := doAuth(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"amandaisle","agent":"claude-code"}`, amanda); rr.Code != http.StatusCreated {
		t.Fatalf("amanda create amandaisle: %d %s", rr.Code, rr.Body)
	}

	// Host owner (no token) sees BOTH.
	if names := islandNames(t, do(t, h, http.MethodGet, "/v1/islands", "")); !names["hostisle"] || !names["amandaisle"] {
		t.Errorf("host owner should see both islands, got %v", names)
	}
	// Amanda sees ONLY her own.
	if names := islandNames(t, doAuth(t, h, http.MethodGet, "/v1/islands", "", amanda)); names["hostisle"] || !names["amandaisle"] {
		t.Errorf("amanda should see only amandaisle, got %v", names)
	}
	// Amanda can't touch the host owner's island (owner gate → 403).
	if rr := doAuth(t, h, http.MethodGet, "/v1/islands/hostisle", "", amanda); rr.Code != http.StatusForbidden {
		t.Errorf("amanda GET hostisle: %d, want 403", rr.Code)
	}

	// Amanda's overview counts only her island + reports her own identity.
	rr := doAuth(t, h, http.MethodGet, "/v1/overview", "", amanda)
	var ov OverviewResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ov)
	if ov.TotalIslands != 1 {
		t.Errorf("amanda overview total_islands = %d, want 1 (only her own)", ov.TotalIslands)
	}
	if ov.Owner != "amanda" || ov.Role != "operator" {
		t.Errorf("amanda overview who-am-i = %q/%q, want amanda/operator", ov.Owner, ov.Role)
	}

	// Host owner's overview counts the whole fleet.
	rr = do(t, h, http.MethodGet, "/v1/overview", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &ov)
	if ov.TotalIslands != 2 {
		t.Errorf("host owner overview total_islands = %d, want 2", ov.TotalIslands)
	}
}
