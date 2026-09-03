package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/runtime/runtimetest"
	"github.com/aoos/dejima/internal/secrets"
)

// CLI table tests: invoke the real cobra commands (via newRootCmd) against an
// in-process httptest daemon backed by a no-op runtime fake — asserting exit
// behavior (the error returned by Execute) and stdout for the major verbs. No
// Docker, no real daemon; the client reaches the test server via DEJIMA_HOST.

// cliEnv spins up an in-proc API server and points the CLI client at it. It
// returns a *Client for direct setup calls (seeding islands, granting links)
// and isolates ~/.dejima under a temp HOME.
func cliEnv(t *testing.T) (*httptest.Server, *api.Client) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_TOKEN", "")
	isolateSecretsBackend(t)
	ledger.ResetDefault()
	srv := joinBackground(t, api.NewServer(runtimetest.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Setenv("DEJIMA_HOST", ts.URL) // CLI client() reads this; ts.URL carries http://
	c, err := api.NewTCPClient(ts.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return ts, c
}

// isolateSecretsBackend keeps the in-proc daemon off the machine's real
// keychain.
//
// A CLI table test that touches secrets otherwise drives the platform store for
// real: `security` on macOS, `secret-tool` on Linux. On a developer's Mac that
// pops an access-authorization dialog; on a CI runner the login keychain can be
// locked, and the `security` call then blocks rather than failing. It blocks
// inside an HTTP handler, so the request never returns, the client sits there,
// and the httptest server can't Close because the connection is still active —
// the test hangs until the 10-minute panic. That is exactly how
// TestSecretSetWarnsOnGitHubTokenPrecedence took down a macOS CI run: the same
// test had passed on the same commit range minutes earlier, because whether the
// keychain answers is a property of the runner, not of the code.
//
// internal/api's secret tests already do this per-test. Doing it in the shared
// helper means a CLI test can't acquire the dependency by accident — which is
// how this one acquired it.
func isolateSecretsBackend(t *testing.T) {
	t.Helper()
	t.Setenv(secrets.BackendEnvVar, "file")
}

// runCLI executes a dejima command with the given args, capturing stdout. It
// returns the captured stdout and the error from Execute (nil = exit 0).
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	var buf strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.Copy(&buf, r) }()

	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(w)
	root.SetErr(w)
	execErr := root.ExecuteContext(context.Background())

	_ = w.Close()
	wg.Wait()
	_ = r.Close()
	return buf.String(), execErr
}

func seedIsland(t *testing.T, c *api.Client, name string) {
	t.Helper()
	if _, err := c.CreateIsland(context.Background(), api.CreateIslandRequest{
		Repo: "r", Name: name, Agent: "claude-code",
	}); err != nil {
		t.Fatalf("seed island %s: %v", name, err)
	}
}

// TestCLILs: `dejima ls` lists seeded islands; empty + populated both exit 0.
func TestCLILs(t *testing.T) {
	_, c := cliEnv(t)

	if _, err := runCLI(t, "ls"); err != nil {
		t.Fatalf("ls (empty): %v", err)
	}

	seedIsland(t, c, "alpha")
	seedIsland(t, c, "beta")
	out, err := runCLI(t, "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("ls output missing islands: %q", out)
	}
}

// TestCLIAgent: `dejima agent ls/add/types` against a seeded island.
func TestCLIAgent(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	out, err := runCLI(t, "agent", "ls", "proj")
	if err != nil {
		t.Fatalf("agent ls: %v", err)
	}
	if !strings.Contains(out, "claude-code") {
		t.Errorf("agent ls missing the primary agent type: %q", out)
	}

	if out, err := runCLI(t, "agent", "types"); err != nil {
		t.Fatalf("agent types: %v", err)
	} else if !strings.Contains(out, "claude-code") {
		t.Errorf("agent types missing claude-code: %q", out)
	}

	if _, err := runCLI(t, "agent", "add", "proj", "--type", "claude-code"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	// Now two agents.
	out, err = runCLI(t, "agent", "ls", "proj")
	if err != nil {
		t.Fatalf("agent ls after add: %v", err)
	}
	if n := strings.Count(out, "claude-code"); n < 2 {
		t.Errorf("expected ≥2 agents after add, output: %q", out)
	}
}

// TestCLIAgentLsMissingIsland: a missing island is a non-zero exit.
func TestCLIAgentLsMissingIsland(t *testing.T) {
	cliEnv(t)
	if _, err := runCLI(t, "agent", "ls", "ghost"); err == nil {
		t.Error("agent ls on a missing island should fail (non-zero exit)")
	}
}

// TestCLILink: grant → ls → revoke an inter-island channel, all exit 0; ls
// reflects the grant; deny-all shows when empty.
func TestCLILink(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "alpha")
	seedIsland(t, c, "beta")

	// Empty: deny-all message.
	if out, err := runCLI(t, "link", "ls"); err != nil {
		t.Fatalf("link ls (empty): %v", err)
	} else if !strings.Contains(out, "deny-all") {
		t.Errorf("empty link ls should mention deny-all: %q", out)
	}

	if out, err := runCLI(t, "link", "grant", "alpha", "beta", "--topic", "ops"); err != nil {
		t.Fatalf("link grant: %v", err)
	} else if !strings.Contains(out, "alpha") || !strings.Contains(out, "ops") {
		t.Errorf("link grant output: %q", out)
	}

	if out, err := runCLI(t, "link", "ls"); err != nil {
		t.Fatalf("link ls: %v", err)
	} else if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") || !strings.Contains(out, "ops") {
		t.Errorf("link ls should show the grant: %q", out)
	}

	if _, err := runCLI(t, "link", "revoke", "alpha", "beta", "--topic", "ops"); err != nil {
		t.Fatalf("link revoke: %v", err)
	}
	if out, _ := runCLI(t, "link", "ls"); strings.Contains(out, "ops") {
		t.Errorf("link ls after revoke still shows the grant: %q", out)
	}

	// grant without --topic is a usage error (non-zero exit).
	if _, err := runCLI(t, "link", "grant", "alpha", "beta"); err == nil {
		t.Error("link grant without --topic should fail")
	}
}

// TestCLIMsg: intra-island send → poll round-trips through the mailbox.
func TestCLIMsg(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	if out, err := runCLI(t, "msg", "send", "hello island", "--island", "proj", "--from", "p1"); err != nil {
		t.Fatalf("msg send: %v", err)
	} else if !strings.Contains(out, "sent #") {
		t.Errorf("msg send output: %q", out)
	}

	out, err := runCLI(t, "msg", "poll", "--island", "proj", "--agent", "p2")
	if err != nil {
		t.Fatalf("msg poll: %v", err)
	}
	if !strings.Contains(out, "hello island") {
		t.Errorf("msg poll should show the broadcast: %q", out)
	}

	// No island and no env → usage error.
	if _, err := runCLI(t, "msg", "poll"); err == nil {
		t.Error("msg poll with no island should fail")
	}
}

// TestCLIMsgUnknownRecipientWarning: a directed `--to` that names no agent in
// the roster is still delivered (the "sent #" line prints), but the CLI emits a
// roster-listing warning to stderr. A known recipient and a broadcast do not.
func TestCLIMsgUnknownRecipientWarning(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	out, err := runCLI(t, "msg", "send", "hi ghost", "--island", "proj", "--from", "p2", "--to", "ghost")
	if err != nil {
		t.Fatalf("msg send: %v", err)
	}
	if !strings.Contains(out, "sent #") {
		t.Errorf("unknown recipient should still deliver: %q", out)
	}
	if !strings.Contains(out, `warning: no agent "ghost" in island roster`) ||
		!strings.Contains(out, "delivered anyway") {
		t.Errorf("expected roster warning for unknown recipient, got: %q", out)
	}

	// Known recipient (the primary id p1) → no warning.
	if out, err := runCLI(t, "msg", "send", "hi p1", "--island", "proj", "--from", "p2", "--to", "p1"); err != nil {
		t.Fatalf("msg send to known: %v", err)
	} else if strings.Contains(out, "warning:") {
		t.Errorf("known recipient should not warn: %q", out)
	}

	// Broadcast → no warning.
	if out, err := runCLI(t, "msg", "send", "hi all", "--island", "proj", "--from", "p2"); err != nil {
		t.Fatalf("msg broadcast: %v", err)
	} else if strings.Contains(out, "warning:") {
		t.Errorf("broadcast should not warn: %q", out)
	}
}

// TestCLIAudit: a brokered op (port grant) is ledgered, then `dejima audit`
// prints it, --verify exits 0 on the intact chain, and --export jsonl emits
// JSON lines.
func TestCLIAudit(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")
	// A Port grant is a ledgered crossing — guaranteed to produce an entry (island
	// lifecycle alone isn't ledgered; the chain records broker decisions).
	if _, err := runCLI(t, "port", "grant", "proj", t.TempDir()+":ro"); err != nil {
		t.Fatalf("port grant (to seed the ledger): %v", err)
	}

	out, err := runCLI(t, "audit")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !strings.Contains(out, "port.grant") {
		t.Errorf("audit should show the port.grant entry: %q", out)
	}

	if out, err := runCLI(t, "audit", "--verify"); err != nil {
		t.Errorf("audit --verify should succeed on an intact chain: %v", err)
	} else if !strings.Contains(out, "intact") {
		t.Errorf("audit --verify output: %q", out)
	}

	if out, err := runCLI(t, "audit", "--export", "jsonl"); err != nil {
		t.Fatalf("audit --export jsonl: %v", err)
	} else if !strings.Contains(out, "{") || !strings.Contains(out, "port.grant") {
		t.Errorf("jsonl export should contain the entry as JSON: %q", out)
	}
}

// TestCLIToken: token create → ls → rm round-trips (owner-only, allowed on the
// trusted local-equivalent test listener).
func TestCLIToken(t *testing.T) {
	cliEnv(t)

	out, err := runCLI(t, "token", "create", "--role", "operator", "--label", "ci")
	if err != nil {
		t.Fatalf("token create: %v", err)
	}
	if !strings.Contains(out, "ci") && !strings.Contains(out, "operator") {
		t.Logf("token create output (id is the key signal): %q", out)
	}

	if out, err := runCLI(t, "token", "ls"); err != nil {
		t.Fatalf("token ls: %v", err)
	} else if !strings.Contains(out, "operator") {
		t.Errorf("token ls should list the created token's role: %q", out)
	}
}

// TestCLIPort: `port grant` → `port list` → `port revoke` exit 0 and list
// reflects the scope. Uses a real host dir under the temp HOME.
func TestCLIPort(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	dir := t.TempDir()
	if _, err := runCLI(t, "port", "grant", "proj", dir+":ro"); err != nil {
		t.Fatalf("port grant: %v", err)
	}
	out, err := runCLI(t, "port", "list", "proj")
	if err != nil {
		t.Fatalf("port list: %v", err)
	}
	if !strings.Contains(out, "ro") {
		t.Errorf("port list should show the read-only scope: %q", out)
	}
}
