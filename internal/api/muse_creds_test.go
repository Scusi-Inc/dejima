package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/paths"
)

// Muse Code authenticates by OIDC device code. Without a pushed seed, every
// island means another browser round-trip — the per-island account
// `dejima auth push` exists to avoid.

func TestPushMuseCredentialsStoresTheSeed(t *testing.T) {
	h, _ := newTestServer(t) // sets HOME to a temp dir

	rr := do(t, h, http.MethodPut, "/v1/credentials/muse",
		`{"credentials_json":"{\"access_token\":\"muse-test\"}"}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("push: %d, body %s", rr.Code, rr.Body.String())
	}
	dir, err := paths.MuseSeedDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, agentcreds.MuseAuthFile)); err != nil {
		t.Fatalf("no auth.json in the seed dir, so MUSE_AUTH_PATH points at nothing: %v", err)
	}
}

// Garbage must be refused rather than stored: a seed is inherited by every
// island, so a bad one fails everywhere at once and looks like a muse bug.
func TestPushMuseCredentialsRejectsGarbage(t *testing.T) {
	h, _ := newTestServer(t)
	for _, body := range []string{
		`{"credentials_json":""}`,
		`{"credentials_json":"not json at all"}`,
		`{"credentials_json":"{}"}`,
	} {
		if rr := do(t, h, http.MethodPut, "/v1/credentials/muse", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s accepted with %d; a bad seed reaches every island", body, rr.Code)
		}
	}
}

// THE LOAD-BEARING MECHANISM. Muse resolves its credential to $MUSE_AUTH_PATH
// before either XDG or ~/.config, and this integration has no shim in the island
// image — the launch line IS the contract. If the export is ever dropped, the
// agent silently falls back to an empty in-container ~/.config/muse and asks
// every island to log in again, which is the exact failure the seed exists to
// prevent and which no other test here would notice.
func TestMuseLaunchPointsAtTheMountedSeed(t *testing.T) {
	spec, ok := handlers.Lookup("muse")
	if !ok {
		t.Fatal("no muse handler registered")
	}
	if !strings.Contains(spec.Launch, "MUSE_AUTH_PATH=/opt/host/muse/auth.json") {
		t.Errorf("launch does not point MUSE_AUTH_PATH at the mount:\n%s", spec.Launch)
	}
	// Guarded, not unconditional: an island with no seed mounted must fall
	// through to muse's own login rather than be handed a path to nothing.
	if !strings.Contains(spec.Launch, "[ -f /opt/host/muse/auth.json ]") {
		t.Errorf("the export is not guarded on the seed existing:\n%s", spec.Launch)
	}
}

// Muse Code is a TERMINAL CODING AGENT — a sibling of claude-code and codex,
// not of openclaw. It gets confused with Meta's consumer "Muse" assistant,
// which is OpenClaw-shaped and hosted. Registering it headless would offer it
// as a home-island brain, which it cannot be: `dejima home` requires a headless
// agent and muse would be accepted and then never serve anything.
//
// The openclaw comparison is the control: it proves this asserts on the Kind
// rather than on a field that happens to be equal for everything.
func TestMuseIsInteractiveNotAHomeIslandBrain(t *testing.T) {
	muse, ok := handlers.Lookup("muse")
	if !ok {
		t.Fatal("no muse handler registered")
	}
	if muse.Kind != handlers.KindInteractive {
		t.Errorf("muse Kind = %v, want KindInteractive", muse.Kind)
	}
	if muse.GatewayPort != 0 {
		t.Errorf("muse has GatewayPort %d; it serves no UI", muse.GatewayPort)
	}
	oc, ok := handlers.Lookup("openclaw")
	if !ok {
		t.Fatal("no openclaw handler registered")
	}
	if oc.Kind != handlers.KindHeadless {
		t.Fatalf("control failed: openclaw Kind = %v, so this test is not reading Kind", oc.Kind)
	}
}
