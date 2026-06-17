package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/hostterm"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// TestHostTerminalsAPI covers the operator host-terminal CRUD: gated off by
// default (403), then create/list/relabel/delete once enabled.
func TestHostTerminalsAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := srv.Handler()

	// Off by default → 403 with a hint.
	if rr := do(t, h, http.MethodGet, "/v1/terminals", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("disabled list: got %d, want 403", rr.Code)
	}
	if rr := do(t, h, http.MethodPost, "/v1/terminals", `{"label":"x"}`); rr.Code != http.StatusForbidden {
		t.Errorf("disabled create: got %d, want 403", rr.Code)
	}

	srv.EnableHostTerminals()

	rr := do(t, h, http.MethodPost, "/v1/terminals", `{"label":"repair"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created hostterm.Terminal
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.ID != "t1" || created.Label != "repair" || created.Tmux() != "dejima-term-t1" {
		t.Fatalf("created = %+v, want t1/repair/dejima-term-t1", created)
	}

	rr = do(t, h, http.MethodPost, "/v1/terminals", ``) // empty body → no label
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.ID != "t2" {
		t.Errorf("second id = %q, want t2 (monotonic)", created.ID)
	}

	rr = do(t, h, http.MethodGet, "/v1/terminals", "")
	var list TerminalsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Terminals) != 2 {
		t.Fatalf("list = %d terminals, want 2", len(list.Terminals))
	}

	if rr := do(t, h, http.MethodPatch, "/v1/terminals/t2", `{"label":"logs"}`); rr.Code != http.StatusOK {
		t.Errorf("relabel: %d, want 200", rr.Code)
	}
	if rr := do(t, h, http.MethodDelete, "/v1/terminals/t1", ""); rr.Code != http.StatusNoContent {
		t.Errorf("delete: %d, want 204", rr.Code)
	}
	if rr := do(t, h, http.MethodDelete, "/v1/terminals/t1", ""); rr.Code != http.StatusNotFound {
		t.Errorf("delete missing: %d, want 404", rr.Code)
	}
}

// TestDeleteGitHubIdentityWarnsAffectedIslands covers #4: deleting an identity
// an island still references returns that island in affected_islands so the
// caller can warn rather than silently changing/losing the island's auth.
func TestDeleteGitHubIdentityWarnsAffectedIslands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := githubid.Update(func(s *githubid.Store) error {
		s.Put(githubid.Identity{Name: "work", Login: "octocat", Token: "tok"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An island that uses the identity, plus one that doesn't.
	if err := (&project.Project{Name: "uses-it", GitHubIdentity: "work"}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&project.Project{Name: "unrelated"}).Save(); err != nil {
		t.Fatal(err)
	}

	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := srv.Handler()

	rr := do(t, h, http.MethodDelete, "/v1/credentials/github/work", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	var resp DeleteGitHubIdentityResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.AffectedIslands) != 1 || resp.AffectedIslands[0] != "uses-it" {
		t.Errorf("affected = %v, want [uses-it]", resp.AffectedIslands)
	}

	// Deleting a never-referenced identity → 404 (and no panic).
	if rr := do(t, h, http.MethodDelete, "/v1/credentials/github/work", ""); rr.Code != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", rr.Code)
	}
}

// TestGitHubReposHandler covers the repos handler at the HTTP layer: identity
// resolution, the Capped passthrough, unknown-identity 404, and upstream-error
// 502. The live GitHub call is replaced via the reposFetch seam; the HTTP/
// base-URL layer itself is covered in internal/githubid/repos_test.go.
func TestGitHubReposHandler(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := githubid.Update(func(s *githubid.Store) error {
		s.Put(githubid.Identity{Name: "work", Login: "octocat", Token: "tok"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	var gotName string
	srv.reposFetch = func(_ context.Context, id githubid.Identity, _ int) (githubid.RepoList, error) {
		gotName = id.Name
		if id.Token != "tok" {
			t.Errorf("handler passed token %q, want tok", id.Token)
		}
		return githubid.RepoList{
			Repos:  []githubid.Repo{{NameWithOwner: "octocat/app", URL: "https://x/app.git"}},
			Capped: true,
		}, nil
	}
	h := srv.Handler()

	rr := do(t, h, http.MethodGet, "/v1/credentials/github/work/repos", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("repos: %d %s", rr.Code, rr.Body.String())
	}
	if gotName != "work" {
		t.Errorf("resolved identity = %q, want work", gotName)
	}
	var resp GitHubReposResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Repos) != 1 || resp.Repos[0].NameWithOwner != "octocat/app" {
		t.Errorf("repos = %+v", resp.Repos)
	}
	if !resp.Capped {
		t.Error("Capped should pass through from the fetch result")
	}

	// Unknown identity → 404.
	if rr := do(t, h, http.MethodGet, "/v1/credentials/github/nope/repos", ""); rr.Code != http.StatusNotFound {
		t.Errorf("unknown identity: %d, want 404", rr.Code)
	}

	// Upstream failure → 502.
	srv.reposFetch = func(context.Context, githubid.Identity, int) (githubid.RepoList, error) {
		return githubid.RepoList{}, fmt.Errorf("github api 401")
	}
	if rr := do(t, h, http.MethodGet, "/v1/credentials/github/work/repos", ""); rr.Code != http.StatusBadGateway {
		t.Errorf("upstream error: %d, want 502", rr.Code)
	}
}

// fakeRuntime implements runtime.Runtime in-memory, recording the exec commands
// the daemon issues and the last CreateContainer request, so a test can assert
// the multi-agent orchestration without a real container engine.
type fakeRuntime struct {
	mu               sync.Mutex
	execs            [][]string
	lastCreate       runtime.CreateRequest
	lastMemoryUpdate [2]string // {container, memory} from UpdateResources
	status           runtime.ContainerStatus
	health           runtime.Health
	volumeSizes      map[string]int64
	volumeCopies     [][2]string
	startCalls       int
	stopCalls        int
	failNewSession   bool // when true, `tmux new-session` exits non-zero
	// execHook, when set, can intercept an Exec call and return a canned
	// (stdout, stderr, exitCode); returning handled=false falls through to the
	// default behavior. Lets a test drive e.g. git-status output.
	execHook func(cmd []string) (stdout, stderr string, code int, handled bool)
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
func (f *fakeRuntime) CopyVolumeData(_ context.Context, src, dst, _ string) error {
	f.mu.Lock()
	f.volumeCopies = append(f.volumeCopies, [2]string{src, dst})
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) EnsureNetwork(context.Context, string) error { return nil }
func (f *fakeRuntime) RemoveNetwork(context.Context, string) error { return nil }
func (f *fakeRuntime) StartContainer(context.Context, string) error {
	f.mu.Lock()
	f.startCalls++
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) StopContainer(context.Context, string) error {
	f.mu.Lock()
	f.stopCalls++
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) RemoveContainer(context.Context, string, bool) error {
	return nil
}
func (f *fakeRuntime) Stats(context.Context, string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (f *fakeRuntime) StatsAll(context.Context) (map[string]runtime.Stats, error) {
	return map[string]runtime.Stats{}, nil
}
func (f *fakeRuntime) VolumeSizes(context.Context) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumeSizes, nil
}
func (f *fakeRuntime) Inspect(context.Context, string) (runtime.Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health, nil
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
func (f *fakeRuntime) UpdateResources(_ context.Context, name, memory string) error {
	f.mu.Lock()
	f.lastMemoryUpdate = [2]string{name, memory}
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Exec(_ context.Context, _ string, cmd []string) (string, string, int, error) {
	f.record(cmd)
	if f.execHook != nil {
		if stdout, stderr, code, handled := f.execHook(cmd); handled {
			return stdout, stderr, code, nil
		}
	}
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
	ledger.ResetDefault()         // re-resolve the ledger under this test's HOME
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

// TestWorkspaceReadyEndpoint asserts the readiness probe reflects whether
// /workspace/.git exists — the signal `dejima connect` waits on so it doesn't
// attach into a still-cloning, empty workspace.
func TestWorkspaceReadyEndpoint(t *testing.T) {
	h, f := newTestServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d, body %s", rr.Code, rr.Body.String())
	}

	// Drive the /workspace/.git probe: absent first (still cloning), then present.
	gitPresent := false
	f.execHook = func(cmd []string) (string, string, int, bool) {
		if len(cmd) == 3 && cmd[0] == "test" && cmd[1] == "-e" && cmd[2] == "/workspace/.git" {
			if gitPresent {
				return "", "", 0, true
			}
			return "", "", 1, true
		}
		return "", "", 0, false
	}

	probe := func() bool {
		rr := do(t, h, http.MethodGet, "/v1/islands/proj/workspace-ready", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("workspace-ready: got %d, body %s", rr.Code, rr.Body.String())
		}
		var out WorkspaceReadyResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Ready
	}

	if probe() {
		t.Error("ready=true while /workspace/.git absent; want false")
	}
	gitPresent = true
	if !probe() {
		t.Error("ready=false after /workspace/.git appeared; want true")
	}

	// Unknown island → 404, not a misleading ready=false.
	if rr := do(t, h, http.MethodGet, "/v1/islands/nope/workspace-ready", ""); rr.Code != http.StatusNotFound {
		t.Errorf("unknown island: got %d, want 404", rr.Code)
	}
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
	if len(island.Agents) != 1 || island.Agents[0].ID != "p1" || island.Agents[0].Tmux != "agent-p1" {
		t.Fatalf("primary agent = %+v, want one {p1 agent-p1}", island.Agents)
	}

	// The container was created with the home volume + registry-sourced launch env.
	f.mu.Lock()
	cr := f.lastCreate
	f.mu.Unlock()
	if cr.Env["DEJIMA_LAUNCH"] != "claude" {
		t.Errorf("DEJIMA_LAUNCH = %q, want claude", cr.Env["DEJIMA_LAUNCH"])
	}
	if cr.Env["DEJIMA_TMUX"] != "agent-p1" {
		t.Errorf("DEJIMA_TMUX = %q, want agent-p1", cr.Env["DEJIMA_TMUX"])
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
	if a2.ID != "p2" || a2.Tmux != "agent-p2" || a2.Branch != "agent/p2" || a2.Worktree != "/workspace/.agents/p2" {
		t.Fatalf("added agent = %+v, want p2/agent-p2/agent/p2/...p2", a2)
	}

	calls := f.calls()
	if !execContains(calls, "git", "-C", "/workspace", "worktree", "add", "/workspace/.agents/p2", "agent/p2") {
		t.Errorf("expected a git worktree add for p2; execs=%v", calls)
	}
	if !execContains(calls, "tmux", "new-session", "agent-p2", "DEJIMA_AGENT_ID=p2", "claude") {
		t.Errorf("expected a tmux new-session running claude for p2; execs=%v", calls)
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
	if a3.ID != "p3" || a3.Attachable {
		t.Fatalf("headless agent = %+v, want p3 non-attachable", a3)
	}
	calls = f.calls()
	if !execContains(calls, "new-session", "agent-p3", headlessLogPath("p3"), "python loop.py", "while true") {
		t.Errorf("expected headless supervised tmux session for p3; execs=%v", calls)
	}

	// A baked-launch headless handler (openclaw) needs no cmd; its launch comes
	// from the registry.
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"openclaw"}`)
	if rr.Code != http.StatusCreated {
		t.Errorf("openclaw without cmd: got %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	var oc AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &oc)
	if oc.Type != "openclaw" || oc.Attachable {
		t.Errorf("openclaw agent = %+v, want type openclaw, non-attachable", oc)
	}
	if !execContains(f.calls(), "new-session", "openclaw gateway") {
		t.Errorf("expected openclaw baked launch in tmux session; execs=%v", f.calls())
	}

	// Per-agent logs for the headless agent stream its log file.
	rr = do(t, h, http.MethodGet, "/v1/islands/proj/logs?agent=p3", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "agent log output") {
		t.Errorf("headless logs: got %d body %q", rr.Code, rr.Body.String())
	}
	if !execContains(f.calls(), "tail", headlessLogPath("p3")) {
		t.Errorf("expected a tail of the p3 log file")
	}

	// Logs for an interactive agent are refused (attach instead).
	rr = do(t, h, http.MethodGet, "/v1/islands/proj/logs?agent=p2", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("interactive agent logs: got %d, want 409", rr.Code)
	}

	// Relabel an agent (cosmetic rename); the id and type are untouched.
	rr = do(t, h, http.MethodPatch, "/v1/islands/proj/agents/p2", `{"label":"frontend"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("relabel p2: got %d, body %s", rr.Code, rr.Body.String())
	}
	var relabeled AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &relabeled)
	if relabeled.ID != "p2" || relabeled.Label != "frontend" {
		t.Fatalf("relabeled = %+v, want p2 labeled frontend", relabeled)
	}
	// An empty label clears it.
	rr = do(t, h, http.MethodPatch, "/v1/islands/proj/agents/p2", `{"label":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear label p2: got %d", rr.Code)
	}
	var cleared AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &cleared)
	if cleared.Label != "" {
		t.Errorf("cleared label = %q, want empty", cleared.Label)
	}
	// Relabeling a missing agent is a 404.
	if rr = do(t, h, http.MethodPatch, "/v1/islands/proj/agents/p9", `{"label":"x"}`); rr.Code != http.StatusNotFound {
		t.Errorf("relabel missing agent: got %d, want 404", rr.Code)
	}

	// Remove the second agent: kills its session, prunes its worktree.
	rr = do(t, h, http.MethodDelete, "/v1/islands/proj/agents/p2", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove p2: got %d", rr.Code)
	}
	calls = f.calls()
	if !execContains(calls, "tmux", "kill-session", "agent-p2") {
		t.Errorf("expected kill-session for p2; execs=%v", calls)
	}
	if !execContains(calls, "worktree", "remove", "/workspace/.agents/p2") {
		t.Errorf("expected worktree remove for p2; execs=%v", calls)
	}

	// The primary agent cannot be removed.
	rr = do(t, h, http.MethodDelete, "/v1/islands/proj/agents/p1", "")
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
		if agents[i].ID == "p2" {
			a2 = &agents[i]
		}
	}
	if a2 == nil {
		t.Fatal("p2 not found")
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
	rr := do(t, h, http.MethodGet, "/v1/islands/hl/agents/h1/session", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("headless session: got %d, want 409 (body %q)", rr.Code, rr.Body.String())
	}
}

// TestCreateAcceptsSeedWithoutRepo covers the no-remote local-copy case: an
// empty repo plus a seed path is a valid clone source (origin stays unset).
func TestCreateAcceptsSeedWithoutRepo(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"name":"seedonly","seed_path":"/tmp/seed-src"}`); rr.Code != http.StatusCreated {
		t.Fatalf("seed-only create: got %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	// But neither repo nor seed is still rejected.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"name":"norepo"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("no repo + no seed: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestGitHubIdentityAPI drives the identity store endpoints: list, seed (put),
// the no-token-leak invariant on read, and delete.
func TestGitHubIdentityAPI(t *testing.T) {
	h, _ := newTestServer(t)

	rr := do(t, h, http.MethodGet, "/v1/credentials/github", "")
	var resp GitHubIdentitiesResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Identities) != 0 {
		t.Fatalf("fresh daemon should have no identities, got %+v", resp.Identities)
	}

	// login + token are required.
	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/bad", `{"login":""}`); rr.Code != http.StatusBadRequest {
		t.Errorf("missing login/token: got %d, want 400", rr.Code)
	}

	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/work", `{"login":"alockwood","token":"ghp_secret","default":true}`); rr.Code != http.StatusOK {
		t.Fatalf("put identity: %d %s", rr.Code, rr.Body.String())
	}

	rr = do(t, h, http.MethodGet, "/v1/credentials/github", "")
	if strings.Contains(rr.Body.String(), "ghp_secret") {
		t.Errorf("list must not leak the token: %s", rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Identities) != 1 || resp.Identities[0].Name != "work" || resp.Identities[0].Login != "alockwood" || !resp.Identities[0].Default {
		t.Fatalf("list = %+v, want one default 'work'/'alockwood'", resp.Identities)
	}

	rr = do(t, h, http.MethodDelete, "/v1/credentials/github/work", "")
	if rr.Code != http.StatusOK {
		t.Errorf("delete: got %d, want 200", rr.Code)
	}
	var del DeleteGitHubIdentityResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &del)
	if len(del.AffectedIslands) != 0 {
		t.Errorf("no islands referenced 'work', got affected = %v", del.AffectedIslands)
	}
	if rr := do(t, h, http.MethodDelete, "/v1/credentials/github/work", ""); rr.Code != http.StatusNotFound {
		t.Errorf("delete missing: got %d, want 404", rr.Code)
	}
}

// TestGitHubIdentityPlumbedIntoIsland verifies that a named daemon GitHub
// identity is validated at create time and materialized into the island as a
// per-island gh config mount (overriding the shared host gh), tokens and all.
func TestGitHubIdentityPlumbedIntoIsland(t *testing.T) {
	h, f := newTestServer(t) // HOME is a temp dir, so the store is isolated

	store := &githubid.Store{}
	store.Put(githubid.Identity{Name: "work", Login: "alockwood", Token: "ghp_work"})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	// An unknown identity is a clean 400, not a provisioning 500.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"bad","github_identity":"nope"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("unknown identity: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj","github_identity":"work"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create with identity: %d %s", rr.Code, rr.Body.String())
	}

	f.mu.Lock()
	cr := f.lastCreate
	f.mu.Unlock()
	var ghMount *runtime.BindMount
	for i := range cr.BindMounts {
		if cr.BindMounts[i].ContainerPath == "/opt/host/gh-config" {
			ghMount = &cr.BindMounts[i]
		}
	}
	if ghMount == nil {
		t.Fatalf("no /opt/host/gh-config mount; binds=%+v", cr.BindMounts)
	}
	if !strings.Contains(ghMount.HostPath, filepath.Join("secrets", "github", "islands", "proj")) {
		t.Errorf("gh mount should be the per-island config dir, got %q", ghMount.HostPath)
	}
	if !ghMount.ReadOnly {
		t.Error("per-island gh config mount must be read-only")
	}
	data, err := os.ReadFile(filepath.Join(ghMount.HostPath, "hosts.yml"))
	if err != nil {
		t.Fatalf("read materialized hosts.yml: %v", err)
	}
	if !strings.Contains(string(data), "oauth_token: ghp_work") || !strings.Contains(string(data), "user: alockwood") {
		t.Errorf("materialized hosts.yml missing the identity's creds:\n%s", data)
	}

	// Deleting the island must remove the materialized token, not just the
	// project dir — it's a live credential living outside ~/.dejima/projects.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/proj", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete island: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(ghMount.HostPath); !os.IsNotExist(err) {
		t.Errorf("per-island gh config should be gone after delete; stat err = %v", err)
	}
}

// TestCreateSeedsMultipleAgents covers seeding >1 agent at create time via the
// Agents field: element 0 is the primary, the rest are co-located agents.
func TestCreateSeedsMultipleAgents(t *testing.T) {
	h, _ := newTestServer(t)
	body := `{"repo":"r","name":"multi","agents":[{"type":"claude-code"},{"type":"codex","label":"backend"}]}`
	rr := do(t, h, http.MethodPost, "/v1/islands", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Agents) != 2 {
		t.Fatalf("want 2 agents, got %d (%+v)", len(info.Agents), info.Agents)
	}
	if info.Agents[0].ID != "m1" || info.Agents[0].Type != "claude-code" {
		t.Errorf("primary = %+v, want m1/claude-code", info.Agents[0])
	}
	if info.Agents[1].ID != "m2" || info.Agents[1].Type != "codex" || info.Agents[1].Label != "backend" {
		t.Errorf("second = %+v, want m2/codex/backend", info.Agents[1])
	}
	if info.Agent != "claude-code" {
		t.Errorf("scalar back-compat agent = %q, want claude-code", info.Agent)
	}

	// A headless extra without a cmd is a clean 400, not a 500.
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"bad","agents":[{"type":"claude-code"},{"type":"headless"}]}`); rr.Code != http.StatusBadRequest {
		t.Errorf("headless extra without cmd: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestHomeIslandRole covers Home Island creation: role=home requires a headless
// brain, an unknown role is rejected, and a valid home island surfaces its role
// and gets the DEJIMA_HOME env so the brain can self-identify.
func TestHomeIslandRole(t *testing.T) {
	h, f := newTestServer(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"badhome","agent":"claude-code","role":"home"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("home+claude-code: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"badrole","agent":"headless","cmd":"x","role":"bogus"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid role: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}

	rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"jarvis","agent":"headless","cmd":"openclaw gateway","role":"home"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create home island: %d %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Role != "home" {
		t.Errorf("role = %q, want home", info.Role)
	}
	if f.lastCreate.Env["DEJIMA_HOME"] != "1" {
		t.Errorf("home island missing DEJIMA_HOME=1 env; got %q", f.lastCreate.Env["DEJIMA_HOME"])
	}

	// A baked-launch headless agent (openclaw) qualifies as a home brain with no
	// cmd — it's non-attachable, so the role gate (attachability, not the literal
	// "headless" type) lets it through. This backs `dejima home create --agent
	// openclaw`.
	rr = do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("home+openclaw: got %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Role != "home" {
		t.Errorf("openclaw home role = %q, want home", info.Role)
	}
	if f.lastCreate.Env["DEJIMA_HOME"] != "1" {
		t.Errorf("openclaw home island missing DEJIMA_HOME=1 env")
	}
}

// TestIslandTitle covers the cosmetic display title: it's editable in place,
// surfaces on GET, and clears with an empty value — Name is never touched.
func TestIslandTitle(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	rr := do(t, h, http.MethodPatch, "/v1/islands/proj", `{"title":"Frontend Rework"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("set title: got %d, body %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Name != "proj" || info.Title != "Frontend Rework" {
		t.Fatalf("after set: name=%q title=%q, want proj / Frontend Rework", info.Name, info.Title)
	}

	// GET reflects the title.
	rr = do(t, h, http.MethodGet, "/v1/islands/proj", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Title != "Frontend Rework" {
		t.Errorf("GET title = %q, want Frontend Rework", info.Title)
	}

	// Empty clears it back to nothing (Name still proj).
	rr = do(t, h, http.MethodPatch, "/v1/islands/proj", `{"title":""}`)
	var cleared IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &cleared)
	if cleared.Title != "" || cleared.Name != "proj" {
		t.Errorf("after clear: name=%q title=%q, want proj / empty", cleared.Name, cleared.Title)
	}

	// Patching a missing island is a 404.
	if rr = do(t, h, http.MethodPatch, "/v1/islands/nope", `{"title":"x"}`); rr.Code != http.StatusNotFound {
		t.Errorf("patch missing island: got %d, want 404", rr.Code)
	}
}

// TestControlSocketNeverMountedIntoIsland is the core of the secure-routing
// change: the daemon's full-control unix socket must never be bind-mounted into
// a container, or in-island code could reach the whole control plane.
func TestControlSocketNeverMountedIntoIsland(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	cr := f.lastCreate
	f.mu.Unlock()
	for _, bm := range cr.BindMounts {
		if strings.Contains(bm.ContainerPath, "dejimad.sock") || strings.Contains(bm.HostPath, "dejimad.sock") {
			t.Fatalf("control socket must never be mounted into an island; got bind %+v", bm)
		}
	}
	if _, ok := cr.Env["DEJIMA_SOCKET"]; ok {
		t.Errorf("DEJIMA_SOCKET env should be gone (no mounted socket)")
	}
}

// TestAutonomyEnvAndExtraHosts verifies that with the in-island token path on,
// every island gets DEJIMA_HOST/DEJIMA_TOKEN and the host.docker.internal route.
func TestAutonomyEnvAndExtraHosts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv.EnableAutonomy("host.docker.internal:7274")
	h := srv.Handler()

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	cr := f.lastCreate
	f.mu.Unlock()

	if cr.Env["DEJIMA_HOST"] != "host.docker.internal:7274" || cr.Env["DEJIMA_TOKEN"] == "" {
		t.Errorf("autonomy env missing: HOST=%q TOKEN-set=%v", cr.Env["DEJIMA_HOST"], cr.Env["DEJIMA_TOKEN"] != "")
	}
	found := false
	for _, eh := range cr.ExtraHosts {
		if eh == "host.docker.internal:host-gateway" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected host.docker.internal:host-gateway in ExtraHosts; got %v", cr.ExtraHosts)
	}
}
