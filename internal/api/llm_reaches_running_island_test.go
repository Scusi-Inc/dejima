package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// llmMount returns the container-side path of the island's LLM bind, or
// "<NO MOUNT>". A missing mount and an empty one are different facts and must
// not collapse into the same value — the whole defect below is that a mount can
// be absent while every other signal says the key is present.
func llmMount(t *testing.T, p *project.Project) string {
	t.Helper()
	binds, err := credentialBindMounts(p)
	if err != nil {
		t.Fatalf("credentialBindMounts: %v", err)
	}
	for _, b := range binds {
		if b.ContainerPath == "/opt/host/llm" {
			return b.HostPath
		}
	}
	return "<NO MOUNT>"
}

// An island created before its operator had ANY provider must still carry the
// /opt/host/llm mount.
//
// credentialBindMounts appended the bind only when islandLLMConfigDir returned
// non-empty, and that function returned "" whenever no agent resolved a
// provider. Bind mounts are decided at container CREATE and nowhere else, so an
// island created in that state had no /opt/host/llm for the rest of its life.
// Registering a provider afterwards wrote the island's key into a host directory
// nothing in the container was looking at.
//
// Every other signal said it had worked: the store held the key, `dejima
// provider ls` listed it, refreshIslandLLMConfigs reported success, and
// agentProviderStatus reported the provider resolved with keySet true. Only the
// agent knew, and all it could say was that it had no key.
//
// This is the case managed local models land in by construction: the operator
// installing a local backend is usually the operator with no provider yet.
func TestLLMMountExistsBeforeAnyProviderDoes(t *testing.T) {
	newTestServer(t) // redirects HOME so ~/.dejima is this test's
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents:       []project.AgentSpec{{ID: "a1", Type: "openclaw"}},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	// The precondition this guard rests on: nothing is configured. If a provider
	// leaked in from somewhere, the mount would appear for the ordinary reason
	// and this test would pass while checking nothing.
	if prov, _, keySet, _ := agentProviderStatus(&p.Agents[0]); keySet {
		t.Fatalf("a provider (%q) resolves in a fixture that is supposed to have none — "+
			"this guard would pass for the wrong reason", prov)
	}
	if got := llmMount(t, p); got == "<NO MOUNT>" {
		t.Error("an island with no provider got no /opt/host/llm mount, so a provider " +
			"registered later can never reach it — only a container recreate can")
	}
}

// Registering a provider AFTER the container exists must reach the agent on a
// restart, not require a recreate.
//
// DEJIMA_PROVIDER_KEY_FILE had one writer: createContainerForProject, from
// p.PrimaryAgent(). That is create-time state, so an operator who registered a
// provider afterwards could restart the agent forever without the variable ever
// changing. Resolving it per agent at launch is what makes the restart real, and
// restartAgentSession routes every agent — the primary included — through here.
//
// The assertion is on the launch line rather than on source text because the
// question is what the agent's process actually receives.
func TestProviderRegisteredAfterCreateReachesARestartedAgent(t *testing.T) {
	h, _ := newTestServer(t)
	a := &project.AgentSpec{ID: "a1", Type: "openclaw", Tmux: "agent-a1"}

	// `/opt/host/llm/` appears only where a provider actually resolved: the
	// handler's own launch string merely DEREFERENCES $DEJIMA_PROVIDER_KEY_FILE,
	// so matching on that name would match the unresolved case too and this
	// precondition would never be able to fail.
	before := agentLaunchScript(a, false)
	if strings.Contains(before, "/opt/host/llm/") {
		t.Fatalf("a key file is named before any provider exists, so the change below "+
			"proves nothing:\n%s", before)
	}

	if rr := do(t, h, http.MethodPut, "/v1/credentials/providers/local",
		`{"api_key":"local","base_url":"http://host.docker.internal:11434/v1","default":true}`); rr.Code != http.StatusOK {
		t.Fatalf("register provider: %d %s", rr.Code, rr.Body.String())
	}

	after := agentLaunchScript(a, false)
	if !strings.Contains(after, "/opt/host/llm/local.env") {
		t.Errorf("the agent restarts with no key file, so a provider registered after "+
			"the container was created still needs a recreate:\n%s", after)
	}
	if !strings.Contains(after, "DEJIMA_PROVIDER='local'") {
		t.Errorf("the agent does not learn WHICH provider it is on; goose reads "+
			"DEJIMA_PROVIDER to set GOOSE_PROVIDER:\n%s", after)
	}
}

// Each agent gets ITS OWN provider, not the primary's.
//
// The container-wide variable was derived from p.PrimaryAgent(), so a
// second agent on a different provider was handed the first one's key file —
// and when the primary was claude-code, which requires no provider key at all,
// the block never ran and the second agent was handed nothing. Its launch line
// guards with `[ -f "$DEJIMA_PROVIDER_KEY_FILE" ]`, so it declined, silently, to
// source a key that was materialized and mounted an inch away.
func TestEachAgentGetsItsOwnProviderNotThePrimarys(t *testing.T) {
	h, _ := newTestServer(t)
	for _, name := range []string{"alpha", "beta"} {
		if rr := do(t, h, http.MethodPut, "/v1/credentials/providers/"+name,
			`{"api_key":"KEY-`+name+`"}`); rr.Code != http.StatusOK {
			t.Fatalf("seed %s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	// alpha went in first, so it is the store default — which is what a
	// claude-code primary would leave a second agent resolving against.
	beta := &project.AgentSpec{ID: "a2", Type: "goose", Provider: "beta"}
	script := agentLaunchScript(beta, false)
	if !strings.Contains(script, "/opt/host/llm/beta.env") {
		t.Errorf("agent a2 asked for beta and did not get it:\n%s", script)
	}
	if strings.Contains(script, "/opt/host/llm/alpha.env") {
		t.Errorf("agent a2 was handed the default provider's key file instead of its own:\n%s", script)
	}
}

// The launch line names the key FILE and never the key.
//
// The whole reason the daemon materializes a 0600 file instead of injecting the
// value is that a container env var shows up in `docker inspect` and in the
// launch command itself. Resolving the provider per agent moved this
// construction to a new place, and a new place is where that property gets lost.
func TestLaunchScriptNeverCarriesTheKeyBytes(t *testing.T) {
	h, _ := newTestServer(t)
	const secret = "sk-DO-NOT-LEAK-THIS"
	if rr := do(t, h, http.MethodPut, "/v1/credentials/providers/openai",
		`{"api_key":"`+secret+`","default":true}`); rr.Code != http.StatusOK {
		t.Fatalf("seed provider: %d %s", rr.Code, rr.Body.String())
	}
	for _, a := range []*project.AgentSpec{
		{ID: "a1", Type: "aider"},                // interactive
		{ID: "a2", Type: "openclaw"},             // headless
		{ID: "a3", Type: "goose", Restart: true}, // headless, supervised
	} {
		script := agentLaunchScript(a, false)
		if !strings.Contains(script, "/opt/host/llm/openai.env") {
			t.Fatalf("agent %s got no key file, so the leak check below has no subject:\n%s", a.ID, script)
		}
		if strings.Contains(script, secret) {
			t.Errorf("agent %s: the API KEY ITSELF is in the launch command, where "+
				"`docker inspect` and every process listing can read it:\n%s", a.ID, script)
		}
	}
}
