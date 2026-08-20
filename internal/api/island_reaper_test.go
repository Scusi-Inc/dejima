package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// The detail view reports whether the island's container reaps orphaned
// processes. It has to ask the RUNTIME: the daemon passes --init today, so the
// daemon's own configuration says every island reaps, while a container created
// before that flag does not — and cannot be changed in place.

// detailReaps creates an island through the API and returns the ReapsOrphans
// field from its detail view.
func detailReaps(t *testing.T, tune func(*fakeRuntime)) *bool {
	t.Helper()
	h, f := newTestServer(t)
	tune(f)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"name":"alpha","repo":"https://github.com/o/r"}`); rr.Code >= 300 {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}
	rr := do(t, h, http.MethodGet, "/v1/islands/alpha", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get island: %d %s", rr.Code, rr.Body.String())
	}
	var out IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return out.ReapsOrphans
}

func TestIslandDetailReportsReapingFromTheRuntime(t *testing.T) {
	for name, want := range map[string]bool{
		"a container the daemon created": true,
		"a container predating --init":   false,
	} {
		t.Run(name, func(t *testing.T) {
			got := detailReaps(t, func(f *fakeRuntime) { f.reapsVal = &want })
			if got == nil {
				t.Fatal("detail should carry a determined answer")
			}
			if *got != want {
				t.Errorf("ReapsOrphans = %v, want %v", *got, want)
			}
		})
	}
}

// The case the pointer exists for. An engine that won't answer must leave the
// field nil so consumers render "not determined" — collapsing it to false would
// accuse a container nobody inspected, and collapsing it to true would hand out
// a clean bill of health nobody earned.
func TestIslandDetailLeavesReapingUnknownWhenTheRuntimeWontSay(t *testing.T) {
	got := detailReaps(t, func(f *fakeRuntime) { f.reapsErr = errors.New("engine unavailable") })
	if got != nil {
		t.Errorf("ReapsOrphans = %v, want nil — an unasked question is not an answer", *got)
	}
}
