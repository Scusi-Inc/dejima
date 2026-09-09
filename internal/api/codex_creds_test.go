package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// A dejima-level Codex account already half-worked: every island's codex shim
// copies /opt/host/codex/auth.json into the agent's ~/.codex, and the daemon
// mounts the host's ~/.codex there. What was missing is a way to GET a login
// onto the daemon host — an operator logged in on the laptop they are typing at,
// driving a Mac mini, had none, because the mini's ~/.codex is empty and nothing
// could fill it remotely. `auth push` solved exactly that for Claude and stopped.

func TestPushCodexCredentialsStoresTheSeed(t *testing.T) {
	h, _ := newTestServer(t) // sets HOME to a temp dir

	rr := do(t, h, http.MethodPut, "/v1/credentials/codex",
		`{"credentials_json":"{\"tokens\":{\"access_token\":\"sk-test\"}}"}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("push: %d, body %s", rr.Code, rr.Body.String())
	}
	dir, err := paths.CodexSeedDir()
	if err != nil {
		t.Fatal(err)
	}
	// THE FILENAME IS LOAD-BEARING. The island shim copies auth.json BY NAME, so
	// a seed written under any other name is invisible to every island already
	// built — a push that reports success and changes nothing.
	blob, err := os.ReadFile(filepath.Join(dir, agentcreds.CodexAuthFile))
	if err != nil {
		t.Fatalf("no auth.json in the seed dir, so no island will ever see it: %v", err)
	}
	if len(blob) == 0 {
		t.Error("the seed is empty")
	}
}

// Garbage must be refused rather than stored. A seed is inherited by every
// island, so storing a truncated read or an unrelated file would break Codex
// everywhere at once, silently.
func TestPushCodexCredentialsRejectsNonsense(t *testing.T) {
	h, _ := newTestServer(t)
	for _, body := range []string{
		`{"credentials_json":""}`,
		`{"credentials_json":"not json at all"}`,
		`{"credentials_json":"{}"}`,
	} {
		if rr := do(t, h, http.MethodPut, "/v1/credentials/codex", body); rr.Code != http.StatusBadRequest {
			t.Errorf("body %s accepted with %d, want 400", body, rr.Code)
		}
	}
}

// The pushed seed is served AT /opt/host/codex — the path the existing shim
// already reads.
//
// This is the decision that makes the feature reach islands built long before
// it. A new mount point would have needed a new shim; the shim ships in the
// image; the image does not reach containers already on disk. Same trap as the
// build-time codex guard, avoidable here for free.
func TestPushedCodexSeedIsMountedWhereTheShimLooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir, err := paths.CodexSeedDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcreds.WriteCodexSeed(dir, []byte(`{"tokens":{"access_token":"x"}}`)); err != nil {
		t.Fatal(err)
	}

	binds, err := credentialBindMounts(&project.Project{Name: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, b := range binds {
		if b.ContainerPath == "/opt/host/codex" {
			got = b.HostPath
			if !b.ReadOnly {
				t.Error("the codex credential is mounted writable")
			}
		}
	}
	if got == "" {
		t.Fatal("nothing is mounted at /opt/host/codex, so the shim copies nothing " +
			"and the pushed login reaches no island")
	}
	if got != dir {
		t.Errorf("mounted %q at /opt/host/codex, want the pushed seed %q — the host's "+
			"own ~/.codex winning would ignore what the operator deliberately pushed", got, dir)
	}
}
