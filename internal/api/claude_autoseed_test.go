package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

const validClaudeBlob = `{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","expiresAt":9999999999}}`

// catHook returns an execHook that answers `cat <claudeCredInIsland>` with blob
// and passes everything else through.
func catHook(blob string) func(cmd []string) (string, string, int, bool) {
	return func(cmd []string) (string, string, int, bool) {
		if len(cmd) == 2 && cmd[0] == "cat" && cmd[1] == claudeCredInIsland {
			return blob, "", 0, true
		}
		return "", "", 0, false
	}
}

func seedFilePath(t *testing.T) string {
	t.Helper()
	dir, err := paths.ClaudeSeedDir()
	if err != nil {
		t.Fatalf("ClaudeSeedDir: %v", err)
	}
	return filepath.Join(dir, ".credentials.json")
}

// makeIsland creates island name via the API (so it's a real, host-owned project)
// and returns it loaded.
func makeIsland(t *testing.T, h http.Handler, name string) *project.Project {
	t.Helper()
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"`+name+`","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create %s: %d", name, rr.Code)
	}
	p, err := project.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return p
}

// TestAutoSeedCapturesWhenUnseeded: an unseeded, host-owned island holding a
// valid login is captured to the host seed, done flips, and the capture is
// surfaced as a credentials.claude.autoseeded event naming the island.
func TestAutoSeedCapturesWhenUnseeded(t *testing.T) {
	srv, h, f := wakeServer(t)
	f.execHook = catHook(validClaudeBlob)
	p := makeIsland(t, h, "isl")
	if p.Owner != project.HostOwner() {
		t.Fatalf("expected the created island to be host-owned; owner=%q", p.Owner)
	}

	srv.maybeAutoSeedClaudeFrom(context.Background(), p)

	got, err := os.ReadFile(seedFilePath(t))
	if err != nil {
		t.Fatalf("seed not written: %v", err)
	}
	if string(got) != validClaudeBlob {
		t.Errorf("seed = %q, want the captured blob", got)
	}
	if !srv.autoSeedDone() {
		t.Error("autoSeedDone should be true after a capture")
	}
	found := false
	for _, e := range srv.IslandEvents("isl") {
		if e.Type == events.TypeCredentialsAutoSeeded {
			found = true
			if e.Payload["source_island"] != "isl" {
				t.Errorf("event payload source_island = %v, want isl", e.Payload["source_island"])
			}
		}
	}
	if !found {
		t.Error("no credentials.claude.autoseeded event emitted")
	}
}

// TestAutoSeedOwnerGate: a login in a NON-host-owned island is never captured
// (its cred must not become the host default).
func TestAutoSeedOwnerGate(t *testing.T) {
	srv, h, f := wakeServer(t)
	f.execHook = catHook(validClaudeBlob)
	p := makeIsland(t, h, "guest")
	p.Owner = "someone-else@laptop" // simulate a teammate/guest island
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	srv.maybeAutoSeedClaudeFrom(context.Background(), p)

	if _, err := os.Stat(seedFilePath(t)); err == nil {
		t.Error("a non-host-owned island's login must NOT be seeded")
	}
	if srv.autoSeedDone() {
		t.Error("owner-gate rejection should not mark done")
	}
}

// TestAutoSeedNoClobber: when the host is already seeded, an island login never
// overwrites it (an explicit `auth push` wins) and the pass self-disables.
func TestAutoSeedNoClobber(t *testing.T) {
	srv, h, f := wakeServer(t)
	f.execHook = catHook(validClaudeBlob) // island offers a DIFFERENT (would-be) blob
	p := makeIsland(t, h, "isl")

	// Pre-seed the host (as `auth push` would).
	existing := `{"claudeAiOauth":{"accessToken":"PUSHED","refreshToken":"r","expiresAt":1}}`
	sp := seedFilePath(t)
	if err := os.MkdirAll(filepath.Dir(sp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	srv.maybeAutoSeedClaudeFrom(context.Background(), p)

	got, _ := os.ReadFile(sp)
	if string(got) != existing {
		t.Errorf("seed was clobbered: %q, want the pushed blob preserved", got)
	}
	if !srv.autoSeedDone() {
		t.Error("an already-seeded host should mark done (self-disable)")
	}
}

// TestAutoSeedValidates: a malformed cred file is never seeded.
func TestAutoSeedValidates(t *testing.T) {
	srv, h, f := wakeServer(t)
	f.execHook = catHook(`{"not":"a-claude-blob"}`) // no claudeAiOauth key
	p := makeIsland(t, h, "isl")

	srv.maybeAutoSeedClaudeFrom(context.Background(), p)

	if _, err := os.Stat(seedFilePath(t)); err == nil {
		t.Error("a malformed credentials blob must NOT be seeded")
	}
	if srv.autoSeedDone() {
		t.Error("a validation failure should not mark done")
	}
}

// TestHostClaudeSeeded: unseeded until a seed file exists.
func TestHostClaudeSeeded(t *testing.T) {
	srv, _, _ := wakeServer(t) // sets HOME to a temp dir (no keychain/file source)
	if srv.hostClaudeSeeded() {
		t.Fatal("host should start unseeded under a fresh HOME")
	}
	sp := seedFilePath(t)
	if err := os.MkdirAll(filepath.Dir(sp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte(validClaudeBlob), 0o600); err != nil {
		t.Fatal(err)
	}
	if !srv.hostClaudeSeeded() {
		t.Error("host should be seeded once the seed file exists")
	}
}
