package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// llmIsland stands up an island whose agent needs a provider key, seeds a
// provider, and materializes the island's copy the way container-create does.
// It returns a reader for one provider's .env.
func llmIsland(t *testing.T, h http.Handler, provider string) (*project.Project, func(string) string) {
	t.Helper()
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents:       []project.AgentSpec{{ID: "a1", Type: "openclaw", Provider: provider}},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	put := `{"api_key":"OLD-KEY","default":true}`
	if rr := do(t, h, http.MethodPut, "/v1/credentials/providers/"+provider, put); rr.Code != http.StatusOK {
		t.Fatalf("seed provider: %d %s", rr.Code, rr.Body.String())
	}
	loaded, err := project.Load("isl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := islandLLMConfigDir(loaded); err != nil {
		t.Fatalf("materialize llm config: %v", err)
	}
	// Read a file from the island's mount. A MISSING file returns the sentinel
	// rather than "" — the first version of the sibling gh test let absent and
	// empty collapse into one value, which made its assertion unfalsifiable.
	read := func(name string) string {
		dir, err := paths.LLMIslandConfigPath("isl")
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				return "<ABSENT>"
			}
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	if !strings.Contains(read(provider+".env"), "OLD-KEY") {
		t.Fatalf("the island never got the seeded key, so nothing below proves anything:\n%s",
			read(provider+".env"))
	}
	return loaded, read
}

// Rotating a provider key must reach islands that ALREADY EXIST.
//
// islandLLMConfigDir ran only from credentialBindMounts — container create — so
// an operator who rotated a key saw `dejima provider ls` report the new one
// while every existing island kept presenting the old one. The symptom is the
// agent failing with "Authentication failed (provider returned HTTP 401)",
// which names the provider and points nowhere near the daemon.
func TestProviderKeyRotationReachesExistingIslands(t *testing.T) {
	h, _ := newTestServer(t)
	_, read := llmIsland(t, h, "anthropic")

	put := `{"api_key":"NEW-KEY","default":true}`
	if rr := do(t, h, http.MethodPut, "/v1/credentials/providers/anthropic", put); rr.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}
	got := read("anthropic.env")
	if strings.Contains(got, "OLD-KEY") {
		t.Errorf("the island is STILL holding the pre-rotation key — the rotation "+
			"reached the store and not the island:\n%s", got)
	}
	if !strings.Contains(got, "NEW-KEY") {
		t.Errorf("the island did not receive the rotated key:\n%s", got)
	}
}

// Deleting a provider must REMOVE the key material, not merely stop rewriting it.
//
// This half is worse than staleness and is why the refresh prunes. A rotation
// that does not propagate fails loudly: the agent 401s and someone looks. A
// REVOKE that does not propagate fails silently and permanently — the store
// forgets the key, every surface agrees it is gone, and the island keeps a
// working copy of it. Nothing ever errors.
func TestProviderDeletionRemovesTheIslandsCopy(t *testing.T) {
	h, _ := newTestServer(t)
	_, read := llmIsland(t, h, "anthropic")

	if rr := do(t, h, http.MethodDelete, "/v1/credentials/providers/anthropic", ""); rr.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	if got := read("anthropic.env"); got != "<ABSENT>" {
		t.Errorf("a REVOKED provider key is still readable inside the island:\n%s", got)
	}
	// The manifest advertises which providers an island has. Leaving it behind
	// tells the agent to go looking for a key that is gone.
	if got := read("providers.json"); got != "<ABSENT>" {
		t.Errorf("the manifest still advertises the revoked provider:\n%s", got)
	}
}

// Repointing an agent at a different provider must materialize that provider's
// key, because the API tells the operator a restart is all that remains.
//
// The response says RestartRequired. Restarting the AGENT does not re-run
// credentialBindMounts — only recreating the CONTAINER did — so the agent came
// back up with no .env for its new provider and failed on a missing key, after
// the daemon reported the change applied and named the finishing action.
func TestAgentProviderChangeMaterializesTheNewKey(t *testing.T) {
	h, _ := newTestServer(t)
	llmIsland(t, h, "anthropic")

	// A second provider exists in the store but has never been needed here.
	if rr := do(t, h, http.MethodPut, "/v1/credentials/providers/openai",
		`{"api_key":"OTHER-KEY"}`); rr.Code != http.StatusOK {
		t.Fatalf("seed second provider: %d %s", rr.Code, rr.Body.String())
	}
	dir, err := paths.LLMIslandConfigPath("isl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openai.env")); !os.IsNotExist(err) {
		t.Fatalf("openai.env exists before the agent was repointed — the test cannot " +
			"distinguish the refresh from the seed")
	}

	if rr := do(t, h, http.MethodPatch, "/v1/islands/isl/agents/a1/config",
		`{"provider":"openai"}`); rr.Code != http.StatusOK {
		t.Fatalf("repoint agent: %d %s", rr.Code, rr.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "openai.env"))
	if err != nil {
		t.Fatalf("the agent's NEW provider key was never materialized, so the "+
			"advertised restart could not have worked: %v", err)
	}
	if !strings.Contains(string(b), "OTHER-KEY") {
		t.Errorf("openai.env holds the wrong key:\n%s", b)
	}
	// And the old one is gone: this island no longer resolves anthropic.
	if _, err := os.Stat(filepath.Join(dir, "anthropic.env")); !os.IsNotExist(err) {
		t.Error("the previous provider's key is still materialized in the island " +
			"after the agent stopped using it")
	}
}
