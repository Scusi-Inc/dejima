package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestIslandIdentityRoundTrip drives the full PUT → GET → DELETE → GET cycle
// through the real handlers: setting a color+glyph returns the updated
// IslandInfo with Identity populated, GET reflects it, DELETE clears it (and the
// updated IslandInfo then omits Identity), and a follow-up GET confirms the
// override is gone. It also asserts both ledger entries land in the hash-chained
// audit log.
func TestIslandIdentityRoundTrip(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"isle","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	// PUT sets the identity and RETURNS the updated IslandInfo.
	rr := do(t, h, http.MethodPut, "/v1/islands/isle/identity", `{"color":"#60a5fa","glyph":"◆"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("set identity: %d %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if info.Identity == nil || info.Identity.Color != "#60a5fa" || info.Identity.Glyph != "◆" {
		t.Fatalf("PUT response identity = %+v, want {#60a5fa ◆}", info.Identity)
	}

	// GET reflects the override.
	rr = do(t, h, http.MethodGet, "/v1/islands/isle", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get island: %d %s", rr.Code, rr.Body.String())
	}
	info = IslandInfo{}
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Identity == nil || info.Identity.Color != "#60a5fa" || info.Identity.Glyph != "◆" {
		t.Fatalf("GET identity = %+v, want {#60a5fa ◆}", info.Identity)
	}

	// DELETE clears it and RETURNS the updated IslandInfo (Identity omitted).
	rr = do(t, h, http.MethodDelete, "/v1/islands/isle/identity", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("clear identity: %d %s", rr.Code, rr.Body.String())
	}
	info = IslandInfo{}
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Identity != nil {
		t.Fatalf("DELETE response identity = %+v, want nil", info.Identity)
	}

	// GET after clear omits the override.
	rr = do(t, h, http.MethodGet, "/v1/islands/isle", "")
	info = IslandInfo{}
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Identity != nil {
		t.Fatalf("post-clear GET identity = %+v, want nil", info.Identity)
	}

	// Ledger recorded both the set and the clear, and the chain verifies.
	rr = do(t, h, http.MethodGet, "/v1/audit", "")
	var ar AuditResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ar)
	var sawSet, sawClear bool
	for _, e := range ar.Entries {
		if e.Island != "isle" {
			continue
		}
		switch e.Type {
		case "identity.set":
			sawSet = true
			if e.Detail != "color=#60a5fa glyph=◆" {
				t.Errorf("identity.set detail = %q, want %q", e.Detail, "color=#60a5fa glyph=◆")
			}
		case "identity.clear":
			sawClear = true
		}
	}
	if !sawSet || !sawClear {
		t.Errorf("ledger missing entries: set=%v clear=%v", sawSet, sawClear)
	}
	if !ar.Verified {
		t.Errorf("ledger chain failed to verify: %s", ar.Error)
	}
}

// TestIslandIdentityValidation rejects malformed color and glyph with 400 and
// leaves no override behind.
func TestIslandIdentityValidation(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"isle","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	bad := []struct {
		name, body string
	}{
		{"bad hex (no hash)", `{"color":"60a5fa","glyph":"◆"}`},
		{"bad hex (wrong length)", `{"color":"#60a5f","glyph":"◆"}`},
		{"bad hex (non-hex digit)", `{"color":"#zzzzzz","glyph":"◆"}`},
		{"empty color", `{"color":"","glyph":"◆"}`},
		{"multi-rune glyph", `{"color":"#60a5fa","glyph":"ab"}`},
		{"empty glyph", `{"color":"#60a5fa","glyph":""}`},
	}
	for _, tc := range bad {
		if rr := do(t, h, http.MethodPut, "/v1/islands/isle/identity", tc.body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400 (%s)", tc.name, rr.Code, rr.Body.String())
		}
	}

	// Short hex (#rgb) and a multi-byte single rune are both valid.
	if rr := do(t, h, http.MethodPut, "/v1/islands/isle/identity", `{"color":"#f0a","glyph":"🏝"}`); rr.Code != http.StatusOK {
		t.Errorf("valid #rgb + emoji glyph: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}

	// A non-existent island is a 404, not a 400.
	if rr := do(t, h, http.MethodPut, "/v1/islands/nope/identity", `{"color":"#60a5fa","glyph":"◆"}`); rr.Code != http.StatusNotFound {
		t.Errorf("missing island: got %d, want 404", rr.Code)
	}
}

// Visual identity is operator-only: a contained island token must never set or
// clear its own identity. Both routes are absent from tokenRouteAccess, so the
// token authorizer denies them (the daemon surface returns 403).
func TestIslandIdentityNotTokenReachable(t *testing.T) {
	if err := authorizeToken("isle", "PUT /v1/islands/{name}/identity", "/v1/islands/isle/identity"); err == nil {
		t.Fatal("setting identity must be denied on the token path")
	}
	if err := authorizeToken("isle", "DELETE /v1/islands/{name}/identity", "/v1/islands/isle/identity"); err == nil {
		t.Fatal("clearing identity must be denied on the token path")
	}
}

// TestIdentityValidators unit-tests the validation helpers directly.
func TestIdentityValidators(t *testing.T) {
	goodColors := []string{"#fff", "#FFF", "#60a5fa", "#FFFFFF", "#000"}
	for _, c := range goodColors {
		if err := validateColor(c); err != nil {
			t.Errorf("validateColor(%q) = %v, want nil", c, err)
		}
	}
	badColors := []string{"", "fff", "#ff", "#ffff", "#fffffff", "#gggggg", "#60a5fa ", "rgb(0,0,0)"}
	for _, c := range badColors {
		if err := validateColor(c); err == nil {
			t.Errorf("validateColor(%q) = nil, want error", c)
		}
	}
	goodGlyphs := []string{"◆", "a", "1", "🏝", "あ"}
	for _, g := range goodGlyphs {
		if err := validateGlyph(g); err != nil {
			t.Errorf("validateGlyph(%q) = %v, want nil", g, err)
		}
	}
	badGlyphs := []string{"", "ab", "◆◆", "a1"}
	for _, g := range badGlyphs {
		if err := validateGlyph(g); err == nil {
			t.Errorf("validateGlyph(%q) = nil, want error", g)
		}
	}
}
