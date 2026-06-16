package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/capability"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/runtime"
)

func writeCapScript(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o700); err != nil { // chmod, not WriteFile perm, to dodge umask
		t.Fatal(err)
	}
}

// capExecServer is newTestServer with a script adapter injected (DefaultAdapter
// errors on macOS, so tests pin their own).
func capExecServer(t *testing.T, capDir string) http.Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	f := &fakeRuntime{status: runtime.StatusRunning}
	srv := NewServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv.capAdapter = &capability.ScriptAdapter{Dir: capDir, Timeout: 3 * time.Second}
	return srv.Handler()
}

func TestCapabilityExecute(t *testing.T) {
	dir := t.TempDir()
	writeCapScript(t, dir, "greet", "#!/bin/sh\nprintf 'island=%s ' \"$DEJIMA_CAP_ISLAND\"\necho \"args=$(cat)\"\n")
	h := capExecServer(t, dir)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	// Deny-all: executing before any grant is 403.
	if rr := do(t, h, http.MethodPost, "/v1/capabilities/execute", `{"island":"oc","target":"greet"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("ungranted execute: got %d, want 403 (%s)", rr.Code, rr.Body.String())
	}

	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/capability/grants", `{"target":"greet"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}

	// Granted: runs, gets args as JSON on stdin + island in env, exit 0.
	rr := do(t, h, http.MethodPost, "/v1/capabilities/execute", `{"island":"oc","target":"greet","args":{"name":"sam"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("execute: %d %s", rr.Code, rr.Body.String())
	}
	var resp CapabilityExecuteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.OK || resp.ExitCode != 0 {
		t.Errorf("resp ok=%v exit=%d, want ok exit 0", resp.OK, resp.ExitCode)
	}
	if !strings.Contains(resp.Output, "island=oc") || !strings.Contains(resp.Output, `args={"name":"sam"}`) {
		t.Errorf("output missing env/stdin: %q", resp.Output)
	}
	if resp.LedgerSeq == 0 {
		t.Error("expected a ledger seq for the execution")
	}

	// A missing-but-granted target fails closed (not found), still ledgered deny.
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/capability/grants", `{"target":"ghost"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant ghost: %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPost, "/v1/capabilities/execute", `{"island":"oc","target":"ghost"}`); rr.Code != http.StatusNotFound {
		t.Errorf("missing target: got %d, want 404", rr.Code)
	}

	// Ledger recorded an allowed execute and the denies.
	rr = do(t, h, http.MethodGet, "/v1/audit", "")
	var ar AuditResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ar)
	var exec, deny bool
	for _, e := range ar.Entries {
		if e.Island != "oc" {
			continue
		}
		switch e.Type {
		case "capability.execute":
			exec = exec || e.Scope == "greet"
		case "capability.deny":
			deny = true
		}
	}
	if !exec {
		t.Error("ledger missing capability.execute for greet")
	}
	if !deny {
		t.Error("ledger missing capability.deny")
	}
	if !ar.Verified {
		t.Errorf("ledger chain failed verify: %s", ar.Error)
	}
}

// The execute route is token-reachable (accessTokenOwn) — an in-island brain
// must be able to call it — unlike the grant routes (operator-only).
func TestCapabilityExecuteTokenReachable(t *testing.T) {
	if err := authorizeToken("oc", "POST /v1/capabilities/execute", "/v1/capabilities/execute"); err != nil {
		t.Fatalf("execute must be allowed on the token path: %v", err)
	}
	if err := authorizeToken("oc", "POST /v1/islands/{name}/capability/grants", "/v1/islands/oc/capability/grants"); err == nil {
		t.Fatal("granting must remain denied on the token path")
	}
}
