package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/localmodel"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/providercreds"
	"github.com/aoos/dejima/internal/runtime"
)

// The curated aliases are what every surface offers, and Ollama knows none of
// them: `pull qwen-coder-7b` stores qwen2.5-coder:7b-instruct-q4_K_M. So an
// agent launched with "local/qwen-coder-7b" asked the backend for a model it
// never had. See launchModel.
func TestLaunchModelResolvesCatalogAliases(t *testing.T) {
	m7b, ok := localmodel.Lookup("qwen-coder-7b")
	if !ok {
		t.Fatal("qwen-coder-7b left the catalog; pick another alias for this test")
	}
	for _, tc := range []struct {
		name, provider, model, want string
	}{
		{"prefixed alias", "local", "local/qwen-coder-7b", "local/" + m7b.Ref},
		{"prefixed alias, provider unset", "", "local/qwen-coder-7b", "local/" + m7b.Ref},
		{"bare alias on the local provider", "local", "qwen-coder-7b", m7b.Ref},
		{"raw ref is left alone", "local", "local/" + m7b.Ref, "local/" + m7b.Ref},
		{"uncurated tag passes through", "local", "local/phi4:latest", "local/phi4:latest"},
		{"another provider is never touched", "openai", "openai/gpt-5.5", "openai/gpt-5.5"},
		{"a bare name on another provider is never touched", "openai", "qwen-coder-7b", "qwen-coder-7b"},
		{"empty stays empty", "local", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchModel(tc.provider, tc.model); got != tc.want {
				t.Errorf("launchModel(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
			}
		})
	}

	// aider's suggested local model is the first thing an operator will try. It
	// has to resolve, or the suggestion is a trap.
	h, _ := handlers.Lookup("aider")
	for _, sm := range h.SuggestedModels {
		if strings.HasPrefix(sm, "local/") && launchModel("local", sm) == sm {
			t.Errorf("aider suggests %q, which no catalog alias resolves — the backend will not have it", sm)
		}
	}
}

func seedLocalProvider(t *testing.T) {
	t.Helper()
	if _, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Put(providercreds.Provider{
			Name: "local", EnvVar: "OPENAI_API_KEY", APIKey: "local",
			BaseURL: "http://host.docker.internal:11434/v1",
		})
		return nil
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
}

// Both launch paths carry the resolved name: agents 1..N through
// agentProviderEnv, and the primary through the container env.
func TestAddedAgentLaunchesWithTheBackendsModelName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedLocalProvider(t)
	m7b, _ := localmodel.Lookup("qwen-coder-7b")
	got := agentProviderEnv(&project.AgentSpec{Type: "aider", Provider: "local", Model: "local/qwen-coder-7b"})
	if !strings.Contains(got, m7b.Ref) {
		t.Errorf("added aider agent launched with %q; want the backend's name %q in it", got, m7b.Ref)
	}
}

// The primary half carried TWO faults. The alias was passed through raw, and
// a bundled primary (codex) given an explicit provider got no provider at all,
// because this path still gated on RequiresProviderKey after #461 fixed the
// same gate for added agents. A codex island created against `local` launched
// on the host's own codex config as if no provider had been chosen.
func TestPrimaryAgentGetsItsProviderAndTheBackendsModelName(t *testing.T) {
	m7b, _ := localmodel.Lookup("qwen-coder-7b")
	for _, typ := range []string{"aider", "codex"} {
		t.Run(typ, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			seedLocalProvider(t)
			p := &project.Project{
				Name:   "loc",
				Agents: []project.AgentSpec{{ID: "l1", Type: typ, Tmux: "agent-l1", Provider: "local", Model: "local/qwen-coder-7b"}},
			}
			f := &fakeRuntime{status: runtime.StatusMissing}
			s := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
			if err := s.createContainerForProject(context.Background(), p, "", false); err != nil {
				t.Fatal(err)
			}
			env := f.lastCreate.Env
			if env["DEJIMA_PROVIDER"] != "local" {
				t.Errorf("primary %s created with DEJIMA_PROVIDER=%q, want \"local\" — the operator's provider was dropped", typ, env["DEJIMA_PROVIDER"])
			}
			if want := "local/" + m7b.Ref; env["DEJIMA_MODEL"] != want {
				t.Errorf("primary %s created with DEJIMA_MODEL=%q, want %q", typ, env["DEJIMA_MODEL"], want)
			}
		})
	}

	// And a bundled primary given NOTHING must launch exactly as before, on the
	// host's own auth and config.
	t.Run("codex with no provider is untouched", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		seedLocalProvider(t)
		p := &project.Project{Name: "plain", Agents: []project.AgentSpec{{ID: "p1", Type: "codex", Tmux: "agent-p1"}}}
		f := &fakeRuntime{status: runtime.StatusMissing}
		s := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
		if err := s.createContainerForProject(context.Background(), p, "", false); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"DEJIMA_PROVIDER", "DEJIMA_MODEL", "DEJIMA_PROVIDER_KEY_FILE"} {
			if v, ok := f.lastCreate.Env[k]; ok {
				t.Errorf("codex with no provider got %s=%q; it must launch on its own config", k, v)
			}
		}
	})
}
