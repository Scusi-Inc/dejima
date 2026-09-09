package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/handlers"
)

// The reported symptom, and what makes it expensive: creating a codex agent put
// the operator on
//
//	dejima@e1c6fd58e4c0:/workspace$
//
// with nothing on screen saying an agent had been meant to run. `npm install -g`
// treats a failed OPTIONAL platform dependency as non-fatal, so the island's
// codex was a wrapper with no executable under it — it exits 1 on any
// invocation, tmux reaps the window, and tmux's own exit code is 0 because the
// FORK succeeded. Every signal the daemon had said yes.

// brokenBinaryHook makes `<bin> --version` fail the way a codex missing its
// platform package actually fails.
func brokenBinaryHook(bin string) func([]string) (string, string, int, bool) {
	return func(cmd []string) (string, string, int, bool) {
		if len(cmd) == 3 && cmd[0] == "bash" && cmd[1] == "-lc" && strings.HasPrefix(cmd[2], bin+" ") {
			return "", "Error: Missing optional dependency @openai/codex-linux-arm64.\n    at findCodexExecutable\n", 1, true
		}
		return "", "", 0, false
	}
}

// sawNewSessionFor reports whether a tmux session was created for tmuxName.
func sawNewSessionFor(f *fakeRuntime, tmuxName string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range f.execs {
		if len(cmd) >= 5 && cmd[0] == "tmux" && cmd[1] == "new-session" && cmd[4] == tmuxName {
			return true
		}
	}
	return false
}

// Creating an agent whose bundled binary cannot run must report WHY, and must
// not leave a tmux session that dies on its own.
func TestAddAgentReportsABundledBinaryThatCannotRun(t *testing.T) {
	h, f := newTestServer(t)
	f.execHook = brokenBinaryHook("codex")
	seedIslandHTTP(t, h, "proj")

	rr := do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"codex","label":"c1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add agent: got %d, body %s", rr.Code, rr.Body.String())
	}
	var a AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}

	// THE ERROR HAS TO REACH THE OPERATOR. setAgentError exists so orchestration
	// failures are not silent; a broken binary was simply never one of them.
	if a.Error == "" {
		t.Fatal("the agent was created with no error recorded, so every surface " +
			"reports it healthy — which is exactly how a bare shell prompt becomes " +
			"the only symptom")
	}
	for _, want := range []string{"cannot run", "dejima image build", "codex"} {
		if !strings.Contains(a.Error, want) {
			t.Errorf("the error is missing %q, so it does not lead anywhere: %s", want, a.Error)
		}
	}
	// The binary's own words are the fastest route to the cause.
	if !strings.Contains(a.Error, "Missing optional dependency") {
		t.Errorf("the probe's stderr is not quoted, so the operator has to go and "+
			"reproduce it by hand: %s", a.Error)
	}
	if sawNewSessionFor(f, a.Tmux) {
		t.Error("a tmux session was created for an agent that cannot start — it dies " +
			"immediately and leaves the operator attaching to a shell")
	}
}

// A WORKING binary must be left completely alone: session created, no error.
//
// The control. Without it this file would pass just as well against a probe that
// refuses every agent, which is a worse bug than the one being fixed.
func TestAddAgentWithAWorkingBinaryIsUnaffected(t *testing.T) {
	h, f := newTestServer(t)
	seedIslandHTTP(t, h, "proj")

	rr := do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"codex","label":"c1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add agent: got %d, body %s", rr.Code, rr.Body.String())
	}
	var a AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Error != "" {
		t.Errorf("a healthy agent was reported broken: %s", a.Error)
	}
	if !sawNewSessionFor(f, a.Tmux) {
		t.Error("no tmux session was created for a perfectly good agent")
	}
}

// A probe that could not RUN is not evidence the binary is broken.
//
// Exec failing (engine hiccup, container mid-stop) says nothing about the
// program. Treating "I could not look" as "it is broken" would let a flake block
// a working agent — the reassuring-direction failure with its sign flipped, and
// the more damaging of the two here.
func TestAProbeThatCannotRunDoesNotBlockTheAgent(t *testing.T) {
	h, f := newTestServer(t)
	seedIslandHTTP(t, h, "proj") // seed first: the island's own setup needs Exec
	f.execErr = errors.New("container is restarting")

	rr := do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"codex","label":"c1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add agent: got %d, body %s", rr.Code, rr.Body.String())
	}
	var a AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Error, "cannot run") {
		t.Errorf("an unreachable probe was reported as a broken binary: %s", a.Error)
	}
}

// Only BUNDLED agents are probed, and the reason is not tidiness.
//
// A tier-2 handler's Launch is shaped `command -v X || install X` — it installs
// on first run, so a missing binary is the NORMAL state of a fresh island.
// Probing one would report a defect on every correct install. And a Launch that
// starts with `bash` must yield nothing at all: `bash --version` always
// succeeds, so the probe would pass forever while proving nothing about the
// agent — a guard aimed at the wrong subject.
func TestOnlyBundledAgentsWithARealBinaryAreProbed(t *testing.T) {
	bundled := map[string]bool{}
	for _, h := range handlers.All() {
		bin := h.PreinstalledBinary()
		if bin == "" {
			continue
		}
		bundled[h.ID] = true
		if !h.Bundled {
			t.Errorf("%s is not bundled but would be probed: a self-installing agent "+
				"is legitimately absent until first launch", h.ID)
		}
		if bin == "bash" || bin == "sh" {
			t.Errorf("%s probes %q, which always succeeds — the check would be "+
				"structurally incapable of failing", h.ID, bin)
		}
	}
	// The two the image promises. If this list changes, the Dockerfile's install
	// line and its build-time `--version` assertions must change with it.
	for _, id := range []string{"codex", "claude-code"} {
		if !bundled[id] {
			t.Errorf("%s is preinstalled by the image but is not probed, so a broken "+
				"one still reaches an operator as a bare shell prompt", id)
		}
	}
}
