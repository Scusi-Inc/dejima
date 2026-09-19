package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/version"
)

// THE GAP THIS CLOSES, stated as the failure rather than the feature.
//
// An agent inside an island asked "what version is the daemon?" and had no way
// to find out. `dejima version` prints version.Version — the binary's own build
// stamp — and an island's binary is frozen in its image, so it reports what the
// ISLAND was built at. `GET /v1/islands/{name}` carries built_version, which is
// the same frozen number wearing a clearer name. /v1/healthz returned
// {"status":"ok"} and nothing else, and it is the only route an island token can
// reach that is not scoped to its own island.
//
// So the agent read its own container's build stamp, called it the daemon's
// version, and repeated it for hours while the daemon moved five releases ahead.
// The reading was never refreshed because there was nothing to refresh it from.
func TestHealthzReportsTheDaemonVersion(t *testing.T) {
	_, h, _ := wakeServer(t)
	rr := do(t, h, http.MethodGet, "/v1/healthz", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rr.Code)
	}
	var got HealthzResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok — the probe's original contract must survive", got.Status)
	}
	if got.Version == "" {
		t.Fatal("no version — an agent still cannot learn what daemon it is talking to")
	}
	// It must be the DAEMON's version. Asserting equality with version.Version is
	// the only check available in-process, and it is the right one: the defect was
	// a caller reporting its OWN stamp as the daemon's, so a field that merely
	// echoes the caller would reproduce the bug with extra steps.
	if got.Version != version.Version {
		t.Errorf("version = %q, want the daemon's own %q", got.Version, version.Version)
	}
}

// The probe is reached by island tokens and by unauthenticated liveness checks
// alike; it must stay parseable by a client that predates the new field, and it
// must not have become a different shape.
func TestHealthzKeepsItsOriginalShape(t *testing.T) {
	_, h, _ := wakeServer(t)
	rr := do(t, h, http.MethodGet, "/v1/healthz", "")
	var raw map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["status"]; !ok {
		t.Error("status disappeared — every existing liveness check reads that key")
	}
	for k := range raw {
		if k != "status" && k != "version" {
			t.Errorf("unexpected key %q on a data-free probe", k)
		}
	}
}
