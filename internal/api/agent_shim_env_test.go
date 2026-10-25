package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/providercreds"
	"github.com/aoos/dejima/internal/runtime"
)

// seedProvider puts a provider in the store under the test's HOME, with a
// base_url so DotEnv emits OPENAI_BASE_URL — the variable init.sh needs.
func seedProvider(t *testing.T, name string) {
	t.Helper()
	_, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Put(providercreds.Provider{
			Name:    name,
			EnvVar:  "OPENAI_API_KEY",
			APIKey:  "local",
			BaseURL: "http://host.docker.internal:11434/v1",
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}
}

// What the per-type shim is run WITH. init.sh branches on DEJIMA_PROVIDER and
// OPENAI_BASE_URL to decide whether to inherit the host's codex config or write
// one for the assigned provider, so a shim run without them does not merely
// lack information — it takes the wrong branch and writes a config that cannot
// start.

// shimCmd returns the `sh -c` script runAgentShim handed the container.
func shimCmd(t *testing.T, a project.AgentSpec) string {
	t.Helper()
	f := &fakeRuntime{status: runtime.StatusRunning}
	s := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	p := &project.Project{Name: "isl", Agents: []project.AgentSpec{a}}
	s.runAgentShim(context.Background(), p, &p.Agents[0])
	for _, c := range f.calls() {
		if len(c) >= 3 && c[0] == "sh" && c[1] == "-c" && strings.Contains(c[2], "init.sh") {
			return c[2]
		}
	}
	t.Fatalf("runAgentShim ran no shim command; calls: %v", f.calls())
	return ""
}

// A bare `VAR=v` prefix applies to the NEXT command only, and the next command
// in this script is the `[ -x ]` test — so the shim exec'd with none of them
// and inherited the container's, which carries the PRIMARY agent's id and no
// worktree at all. The comment claiming the id was "scoped to this agent" had
// never been true.
func TestAgentShimActuallyExportsItsVariables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := shimCmd(t, project.AgentSpec{ID: "d3", Type: "codex", Worktree: "/workspace/.agents/d3"})

	// The failing shape, named so a revert is recognised rather than puzzled over.
	if strings.Contains(got, "DEJIMA_AGENT_ID=d3 DEJIMA_WORKTREE='/workspace/.agents/d3' [") {
		t.Errorf("variables are prefixed onto the `[ -x ]` test, so the shim gets "+
			"none of them:\n%s", got)
	}
	if !strings.Contains(got, "export ") {
		t.Errorf("shim env must be exported to survive to the exec:\n%s", got)
	}
	for _, want := range []string{"DEJIMA_AGENT_ID='d3'", "DEJIMA_WORKTREE='/workspace/.agents/d3'"} {
		if !strings.Contains(got, want) {
			t.Errorf("shim command lacks %s:\n%s", want, got)
		}
	}
}

// A co-located agent with its own provider. The container-wide DEJIMA_PROVIDER
// is the PRIMARY's, set once at create, so this agent's provider has to travel
// with the shim invocation or codex's shim writes the host-inheriting config.
func TestAgentShimCarriesThisAgentsProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedProvider(t, "local")
	got := shimCmd(t, project.AgentSpec{ID: "d3", Type: "codex", Provider: "local",
		Worktree: "/workspace/.agents/d3"})

	if !strings.Contains(got, "DEJIMA_PROVIDER='local'") {
		t.Errorf("an explicitly assigned provider must reach the shim:\n%s", got)
	}
	// OPENAI_BASE_URL never exists as container env by design — the key bytes
	// stay in the 0600 mounted file — so the shim has to source it.
	if !strings.Contains(got, "DEJIMA_PROVIDER_KEY_FILE") || !strings.Contains(got, "set -a") {
		t.Errorf("the shim must source the provider key file, which is the only "+
			"place OPENAI_BASE_URL exists:\n%s", got)
	}
}

// Ordering: the key file must be sourced BEFORE the shim execs, not after.
func TestAgentShimSourcesTheKeyBeforeExec(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedProvider(t, "local")
	got := shimCmd(t, project.AgentSpec{ID: "d3", Type: "codex", Provider: "local"})
	src, ex := strings.Index(got, "set -a"), strings.Index(got, "exec ")
	if src < 0 || ex < 0 || src > ex {
		t.Errorf("key file is not sourced before the exec (src=%d exec=%d):\n%s", src, ex, got)
	}
}

// The other end of the same chain: the key file the shim sources has to exist.
//
// islandLLMConfigDir materialized /opt/host/llm/<prov>.env only for agents
// whose handler sets RequiresProviderKey. codex is Bundled and sets it false —
// it authenticates itself — so an operator who explicitly pointed a codex agent
// at `local` got no file at all, and every consumer downstream was a guarded
// no-op: the launch line's `[ -f "$_dk" ]`, and now the shim's. Nothing
// reported an error; codex simply ran on the host's config.
func TestExplicitProviderMaterializesTheKeyFileForABundledAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedProvider(t, "local")
	p := &project.Project{
		Name:   "isl",
		Agents: []project.AgentSpec{{ID: "d3", Type: "codex", Provider: "local"}},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	dir, err := islandLLMConfigDir(p)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "local.env"))
	if err != nil {
		t.Fatalf("codex was given --provider local and got no key file: %v", err)
	}
	// OPENAI_BASE_URL is the variable init.sh needs to write a provider config;
	// without it the provider branch cannot fire even with DEJIMA_PROVIDER set.
	if !strings.Contains(string(b), "OPENAI_BASE_URL=") {
		t.Errorf("key file carries no OPENAI_BASE_URL:\n%s", b)
	}
}

// And the gate still holds for an agent that was given NO provider: a Bundled
// agent on its own OAuth must not drag a key file in behind it.
func TestBundledAgentWithoutAProviderStillMaterializesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedProvider(t, "local")
	p := &project.Project{
		Name:   "isl",
		Agents: []project.AgentSpec{{ID: "d3", Type: "codex"}},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	dir, err := islandLLMConfigDir(p)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "local.env")); err == nil {
		t.Error("no provider was assigned, so no key belongs in this island")
	}
}
