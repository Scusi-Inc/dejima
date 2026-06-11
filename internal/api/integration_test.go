package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aoos/dejima/internal/runtime"
)

// fakeRuntime implements runtime.Runtime in-memory, recording the exec commands
// the daemon issues and the last CreateContainer request, so a test can assert
// the multi-agent orchestration without a real container engine.
type fakeRuntime struct {
	mu             sync.Mutex
	execs          [][]string
	lastCreate     runtime.CreateRequest
	status         runtime.ContainerStatus
	failNewSession bool // when true, `tmux new-session` exits non-zero
}

func (f *fakeRuntime) record(cmd []string) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	f.mu.Unlock()
}

func (f *fakeRuntime) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.execs))
	copy(out, f.execs)
	return out
}

func (f *fakeRuntime) ImageExists(context.Context, string) (bool, error) { return true, nil }
func (f *fakeRuntime) EnsureVolume(context.Context, string) error        { return nil }
func (f *fakeRuntime) RemoveVolume(context.Context, string, bool) error  { return nil }
func (f *fakeRuntime) EnsureNetwork(context.Context, string) error       { return nil }
func (f *fakeRuntime) RemoveNetwork(context.Context, string) error       { return nil }
func (f *fakeRuntime) StartContainer(context.Context, string) error      { return nil }
func (f *fakeRuntime) StopContainer(context.Context, string) error       { return nil }
func (f *fakeRuntime) RemoveContainer(context.Context, string, bool) error {
	return nil
}
func (f *fakeRuntime) Stats(context.Context, string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (f *fakeRuntime) StatsAll(context.Context) (map[string]runtime.Stats, error) {
	return map[string]runtime.Stats{}, nil
}
func (f *fakeRuntime) Inspect(context.Context, string) (runtime.Health, error) {
	return runtime.Health{}, nil
}
func (f *fakeRuntime) Status(context.Context, string) (runtime.ContainerStatus, error) {
	return f.status, nil
}
func (f *fakeRuntime) CreateContainer(_ context.Context, req runtime.CreateRequest) (string, error) {
	f.mu.Lock()
	f.lastCreate = req
	f.mu.Unlock()
	return "cid", nil
}
func (f *fakeRuntime) Exec(_ context.Context, _ string, cmd []string) (string, string, int, error) {
	f.record(cmd)
	switch {
	case len(cmd) >= 3 && cmd[0] == "test" && strings.Contains(cmd[2], "/.agents/") && strings.HasSuffix(cmd[2], "/.git"):
		return "", "", 1, nil // agent worktree not created yet
	case len(cmd) >= 3 && cmd[0] == "test" && cmd[2] == "/workspace/.git":
		return "", "", 0, nil // primary repo present
	case len(cmd) >= 2 && cmd[0] == "tmux" && cmd[1] == "has-session":
		return "", "", 1, nil // session not running yet
	case len(cmd) >= 2 && cmd[0] == "tmux" && cmd[1] == "new-session" && f.failNewSession:
		return "", "no server running on /tmp/tmux", 1, nil
	default:
		return "", "", 0, nil
	}
}
func (f *fakeRuntime) ExecStream(_ context.Context, _ string, cmd []string) (io.ReadCloser, error) {
	f.record(cmd)
	return io.NopCloser(strings.NewReader("agent log output\n")), nil
}
func (f *fakeRuntime) BuildImage(context.Context, string, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeRuntime) CopyToContainer(context.Context, string, string, string) error   { return nil }
func (f *fakeRuntime) CopyFromContainer(context.Context, string, string, string) error { return nil }
func (f *fakeRuntime) Logs(context.Context, string, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("container logs\n")), nil
}

// execContains reports whether any recorded exec command contains all the given
// substrings (joined by space) somewhere in its joined argv.
func execContains(calls [][]string, parts ...string) bool {
outer:
	for _, c := range calls {
		joined := strings.Join(c, " ")
		for _, p := range parts {
			if !strings.Contains(joined, p) {
				continue outer
			}
		}
		return true
	}
	return false
}

func newTestServer(t *testing.T) (http.Handler, *fakeRuntime) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // redirect ~/.dejima to a temp dir
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return srv.Handler(), f
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, r)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestMultiAgentLifecycle drives create → add → list → logs → remove through the
// real HTTP handlers, asserting the daemon issues the right container env and
// exec commands for the multi-agent machinery.
func TestMultiAgentLifecycle(t *testing.T) {
	h, f := newTestServer(t)

	// Create a single-agent island.
	rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d, body %s", rr.Code, rr.Body.String())
	}
	var island IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &island); err != nil {
		t.Fatal(err)
	}
	if len(island.Agents) != 1 || island.Agents[0].ID != "a1" || island.Agents[0].Tmux != "agent-a1" {
		t.Fatalf("primary agent = %+v, want one {a1 agent-a1}", island.Agents)
	}

	// The container was created with the home volume + registry-sourced launch env.
	f.mu.Lock()
	cr := f.lastCreate
	f.mu.Unlock()
	if cr.Env["DEJIMA_LAUNCH"] != "claude" {
		t.Errorf("DEJIMA_LAUNCH = %q, want claude", cr.Env["DEJIMA_LAUNCH"])
	}
	if cr.Env["DEJIMA_TMUX"] != "agent-a1" {
		t.Errorf("DEJIMA_TMUX = %q, want agent-a1", cr.Env["DEJIMA_TMUX"])
	}
	if cr.Env["DEJIMA_AGENT"] != "claude-code" {
		t.Errorf("DEJIMA_AGENT = %q, want claude-code", cr.Env["DEJIMA_AGENT"])
	}
	foundHome := false
	for _, v := range cr.Volumes {
		if v.Target == "/home/dejima" && v.Name == "dejima-proj-home" {
			foundHome = true
		}
	}
	if !foundHome {
		t.Errorf("container missing /home/dejima home volume mount; volumes=%+v", cr.Volumes)
	}

	// Add a second interactive agent.
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"claude-code","label":"frontend"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add agent: got %d, body %s", rr.Code, rr.Body.String())
	}
	var a2 AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &a2)
	if a2.ID != "a2" || a2.Tmux != "agent-a2" || a2.Branch != "agent/a2" || a2.Worktree != "/workspace/.agents/a2" {
		t.Fatalf("added agent = %+v, want a2/agent-a2/agent/a2/...a2", a2)
	}

	calls := f.calls()
	if !execContains(calls, "git", "-C", "/workspace", "worktree", "add", "/workspace/.agents/a2", "agent/a2") {
		t.Errorf("expected a git worktree add for a2; execs=%v", calls)
	}
	if !execContains(calls, "tmux", "new-session", "agent-a2", "DEJIMA_AGENT_ID=a2", "claude") {
		t.Errorf("expected a tmux new-session running claude for a2; execs=%v", calls)
	}

	// List shows both agents.
	rr = do(t, h, http.MethodGet, "/v1/islands/proj/agents", "")
	var agents []AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &agents)
	if len(agents) != 2 {
		t.Fatalf("list agents = %d, want 2", len(agents))
	}

	// Headless requires a command.
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"headless"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("headless without cmd: got %d, want 400", rr.Code)
	}

	// Headless with a command runs in tmux with a per-agent log + restart loop.
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"headless","cmd":"python loop.py"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add headless: got %d, body %s", rr.Code, rr.Body.String())
	}
	var a3 AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &a3)
	if a3.ID != "a3" || a3.Attachable {
		t.Fatalf("headless agent = %+v, want a3 non-attachable", a3)
	}
	calls = f.calls()
	if !execContains(calls, "new-session", "agent-a3", headlessLogPath("a3"), "python loop.py", "while true") {
		t.Errorf("expected headless supervised tmux session for a3; execs=%v", calls)
	}

	// Per-agent logs for the headless agent stream its log file.
	rr = do(t, h, http.MethodGet, "/v1/islands/proj/logs?agent=a3", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "agent log output") {
		t.Errorf("headless logs: got %d body %q", rr.Code, rr.Body.String())
	}
	if !execContains(f.calls(), "tail", headlessLogPath("a3")) {
		t.Errorf("expected a tail of the a3 log file")
	}

	// Logs for an interactive agent are refused (attach instead).
	rr = do(t, h, http.MethodGet, "/v1/islands/proj/logs?agent=a2", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("interactive agent logs: got %d, want 409", rr.Code)
	}

	// Remove the second agent: kills its session, prunes its worktree.
	rr = do(t, h, http.MethodDelete, "/v1/islands/proj/agents/a2", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove a2: got %d", rr.Code)
	}
	calls = f.calls()
	if !execContains(calls, "tmux", "kill-session", "agent-a2") {
		t.Errorf("expected kill-session for a2; execs=%v", calls)
	}
	if !execContains(calls, "worktree", "remove", "/workspace/.agents/a2") {
		t.Errorf("expected worktree remove for a2; execs=%v", calls)
	}

	// The primary agent cannot be removed.
	rr = do(t, h, http.MethodDelete, "/v1/islands/proj/agents/a1", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("remove primary: got %d, want 409", rr.Code)
	}
}

// TestAgentOrchestrationErrorSurfaced verifies that when an agent's session
// fails to come up, the reason is captured on AgentInfo (not just logged).
func TestAgentOrchestrationErrorSurfaced(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	f.failNewSession = true
	f.mu.Unlock()

	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("add agent: %d %s", rr.Code, rr.Body.String()) // add still succeeds; the launch failure is surfaced, not fatal
	}
	rr := do(t, h, http.MethodGet, "/v1/islands/proj/agents", "")
	var agents []AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &agents)
	var a2 *AgentInfo
	for i := range agents {
		if agents[i].ID == "a2" {
			a2 = &agents[i]
		}
	}
	if a2 == nil {
		t.Fatal("a2 not found")
	}
	if a2.Error == "" || !strings.Contains(a2.Error, "tmux new-session") {
		t.Errorf("a2.Error = %q, want it to mention the tmux new-session failure", a2.Error)
	}
}

// TestSessionResolvesHeadless409 verifies the attach route refuses a headless
// agent with a clear error (the WS handshake fails fast before upgrading).
func TestHeadlessAgentNotAttachable(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"hl","agent":"headless","cmd":"python x.py"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create headless island: %d %s", rr.Code, rr.Body.String())
	}
	// The headless primary is not attachable; the session route reports 409.
	rr := do(t, h, http.MethodGet, "/v1/islands/hl/agents/a1/session", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("headless session: got %d, want 409 (body %q)", rr.Code, rr.Body.String())
	}
}
