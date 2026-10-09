package api

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/providercreds"
)

// A BUNDLED AGENT CAN STILL BE GIVEN A PROVIDER.
//
// agentProviderEnv used to gate on RequiresProviderKey alone, which means "this
// framework cannot work without a key". codex and claude-code authenticate
// themselves, so they require nothing — and an operator who pointed codex at a
// local model got no DEJIMA_PROVIDER_KEY_FILE at all, silently. The provider was
// resolved, materialized and mounted; the agent was simply never told.
//
// registerLocalProvider's own comment says OPENAI_API_KEY + OPENAI_BASE_URL is
// "the shape OpenAI-compatible agents (aider, codex, goose, …) already read", so
// this was always meant to work. An explicit provider is an instruction.
func TestAgentProviderEnv_ExplicitProviderOnBundledAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Put(providercreds.Provider{
			Name: "local", EnvVar: "OPENAI_API_KEY", APIKey: "local",
			BaseURL: "http://host.docker.internal:11434/v1",
		})
		return nil
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	h, ok := handlers.Lookup("codex")
	if !ok {
		t.Fatal("no codex handler")
	}
	if h.RequiresProviderKey {
		t.Skip("codex now requires a key; this test guards the bundled case")
	}

	got := agentProviderEnv(&project.AgentSpec{Type: "codex", Provider: "local", Model: "local/qwen-coder"})
	for _, want := range []string{"DEJIMA_PROVIDER=", "DEJIMA_PROVIDER_KEY_FILE=", "DEJIMA_MODEL="} {
		if !strings.Contains(got, want) {
			t.Errorf("explicit provider on codex produced %q, want it to contain %q", got, want)
		}
	}

	// The ordinary ChatGPT case must be untouched: no provider, no variables,
	// so the host's own auth and config keep working exactly as before.
	if plain := agentProviderEnv(&project.AgentSpec{Type: "codex"}); plain != "" {
		t.Errorf("codex with no provider produced %q, want empty — a bundled agent "+
			"that was given nothing must be launched as it always was", plain)
	}
}

// THE VARIABLE IS USELESS IF NOTHING READS IT.
//
// The key bytes stay in a 0600 mounted file and never become container env, so
// something has to source it. That used to be each handler's own launch line,
// which meant a bundled agent given a provider got the path and read nothing.
// It is now done once, centrally, for every interactive agent — and this holds
// that down, because the failure is invisible: the provider resolves, the file
// is materialized and mounted, and the agent just never sees a key.
func TestAgentLaunchScriptSourcesTheProviderKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Put(providercreds.Provider{
			Name: "local", EnvVar: "OPENAI_API_KEY", APIKey: "local",
			BaseURL: "http://host.docker.internal:11434/v1",
		})
		return nil
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	for _, typ := range []string{"codex", "aider"} {
		script := agentLaunchScript(&project.AgentSpec{ID: "a1", Type: typ, Provider: "local"}, false)
		if !strings.Contains(script, "DEJIMA_PROVIDER_KEY_FILE") {
			t.Errorf("%s launch script never mentions DEJIMA_PROVIDER_KEY_FILE:\n%s", typ, script)
			continue
		}
		// Naming the path is not reading it: the bug this guards against is a
		// script that exports the PATH and never sources the file.
		if !strings.Contains(script, `&& . "$_dk"`) && !strings.Contains(script, `&& . "$k"`) {
			t.Errorf("%s launch script names the key file but never sources it:\n%s", typ, script)
		}
	}
}

// A Bundled handler's Launch must START WITH ITS OWN BINARY:
// TestBundledLaunchCommandsAreAcceptedByTheirBinaries takes fields[0] and
// probes it. Wrapping codex's launch in `bash -lc` to source a key made that
// binary "bash" and broke five tests at once — which is why the sourcing moved
// to agentLaunchScript instead.
func TestBundledLaunchesStartWithTheirBinary(t *testing.T) {
	for _, h := range handlers.All() {
		if !h.Bundled || h.Launch == "" {
			continue
		}
		if first := strings.Fields(h.Launch)[0]; first == "bash" || first == "sh" {
			t.Errorf("bundled handler %q launches via %q; callers take fields[0] as the "+
				"binary to probe and repair", h.ID, first)
		}
	}
}
