package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Attaching before the entrypoint has started the agent must not REPLACE the
// agent with a bare shell — permanently.
//
// The attach runs `tmux new-session -A`, which creates the session when it is
// missing. image/start.sh then guards its own launch with `if ! tmux
// has-session`, finds the name already taken, and never starts the agent at all.
// So a connection a second too early does not merely show the wrong thing once:
// the island comes up looking healthy with a login shell where its agent should
// be, and stays that way until something recreates the container.
//
// The window is easy to hit exactly when it matters — recreate an island to
// apply a secret, and the dashboard reattaches while it is still booting.
//
// This asserts the daemon LAUNCHES the agent rather than letting the attach
// conjure an empty session. It drives the pre-attach path directly: the
// websocket upgrade needs a real hijackable connection, which httptest's
// recorder is not, and the decision under test happens before it.
func TestAttachStartsTheAgentInsteadOfConjuringAShell(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := joinBackground(t, NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))

	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Agents: []project.AgentSpec{
			{ID: "a1", Type: "claude-code", Tmux: "agent-a1"},
			{ID: "a2", Type: "claude-code", Tmux: "agent-a2"},
		},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	// The container is up but the entrypoint has NOT created the session yet.
	f.mu.Lock()
	f.execHook = func(cmd []string) (string, string, int, bool) {
		if len(cmd) >= 3 && cmd[0] == "tmux" && cmd[1] == "has-session" {
			return "", "", 1, true // no session yet — the race window
		}
		return "", "", 0, false
	}
	f.execs = nil
	f.mu.Unlock()

	srv.ensureAttachTarget(t.Context(), p, &p.Agents[1], "agent-a2")

	f.mu.Lock()
	execs := append([][]string(nil), f.execs...)
	f.mu.Unlock()

	var launched bool
	for _, c := range execs {
		if len(c) >= 4 && c[0] == "tmux" && c[1] == "new-session" && strings.Contains(strings.Join(c, " "), "agent-a2") {
			launched = true
		}
	}
	if !launched {
		t.Fatalf("the daemon did not start agent a2 before the attach, so `new-session -A` "+
			"would create an empty shell under that name and start.sh would skip the agent "+
			"forever. execs=%v", execs)
	}
}

// The helper working is not the same as the attach path using it.
//
// The first version of the test above called ensureAttachTarget directly, so
// DELETING THE CALL FROM sessionWS changed nothing and the suite stayed green —
// a guard that proves a function is correct while the code walks past it. The
// behaviour cannot be driven end-to-end here (the websocket upgrade needs a
// hijackable connection a recorder cannot provide), so assert the wiring at the
// source level instead of pretending to assert it at runtime.
func TestSessionAttachActuallyCallsEnsureAttachTarget(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "session.go", nil, 0)
	if err != nil {
		t.Fatalf("parse session.go: %v", err)
	}
	var body *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "sessionWS" {
			body = fd
		}
	}
	if body == nil {
		t.Fatal("sessionWS not found — it was renamed, and this guard now checks nothing " +
			"while passing")
	}
	called := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if se, ok := ce.Fun.(*ast.SelectorExpr); ok {
				called[se.Sel.Name] = true
			}
		}
		return true
	})
	// Each of these is unit-tested on its own, and a unit test cannot notice the
	// CALL being deleted — which is how the first version of this file passed
	// while sessionWS walked straight past the helper it was proving correct.
	for _, req := range []struct{ fn, why string }{
		{"ensureAttachTarget",
			"`tmux new-session -A` will create an EMPTY shell under the agent's session " +
				"name when the attach beats the entrypoint, and start.sh then skips " +
				"launching the agent permanently"},
		{"resolveAttachSize",
			"the attach can come up at 0x0, and under `window-size latest` that client " +
				"becomes the latest one and collapses the shared window — the black pane"},
	} {
		if !called[req.fn] {
			t.Errorf("sessionWS does not call %s, so %s", req.fn, req.why)
		}
	}
}
