package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/mcpbroker"
	"github.com/aoos/dejima/internal/runtime"
)

// fakeBroker stands in for a real MCP server so the handler can be exercised
// without spawning a process. It records calls and returns canned results.
type fakeBroker struct {
	calls  []mcpbroker.Request
	result mcpbroker.Result
	err    error
}

func (f *fakeBroker) Name() string { return "fake" }
func (f *fakeBroker) Call(_ context.Context, req mcpbroker.Request) (mcpbroker.Result, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}

// mcpServer builds a test server with the MCP broker stubbed by fb.
func mcpServer(t *testing.T, fb *fakeBroker) http.Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	prev := newMCPBroker
	newMCPBroker = func() (mcpbroker.Broker, error) { return fb, nil }
	t.Cleanup(func() { newMCPBroker = prev })
	srv := joinBackground(t, NewServer(&fakeRuntime{status: runtime.StatusRunning}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	return srv.Handler()
}

func TestMCPCall_DenyAllThenGrant(t *testing.T) {
	fb := &fakeBroker{result: mcpbroker.Result{Output: json.RawMessage(`{"tools":[{"name":"echo"}]}`)}}
	h := mcpServer(t, fb)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}

	// Deny-all: a call before any grant is 403 and the broker is never reached.
	if rr := do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"files","method":"tools/list"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("ungranted call: got %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
	if len(fb.calls) != 0 {
		t.Fatalf("broker reached before a grant: %+v", fb.calls)
	}

	// Grant the server.
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}

	// Granted: the call reaches the broker and returns its result + a ledger seq.
	rr := do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"files","method":"tools/list"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("call: %d %s", rr.Code, rr.Body.String())
	}
	var resp MCPCallResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.OK || resp.IsError {
		t.Errorf("resp ok=%v isError=%v, want ok", resp.OK, resp.IsError)
	}
	if string(resp.Result) == "" || resp.LedgerSeq == 0 {
		t.Errorf("missing result/seq: %s seq=%d", resp.Result, resp.LedgerSeq)
	}
	if len(fb.calls) != 1 || fb.calls[0].Server != "files" || fb.calls[0].Method != "tools/list" {
		t.Fatalf("broker call mismatch: %+v", fb.calls)
	}
}

func TestMCPCall_MethodNotAllowed(t *testing.T) {
	fb := &fakeBroker{}
	h := mcpServer(t, fb)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`)
	do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`)

	// A lifecycle method is outside the brokered surface — rejected before the broker.
	rr := do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"files","method":"initialize"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("initialize: got %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if len(fb.calls) != 0 {
		t.Fatalf("broker reached for a disallowed method: %+v", fb.calls)
	}
}

func TestMCPCall_BrokerErrorMapped(t *testing.T) {
	fb := &fakeBroker{err: mcpbroker.ErrServerNotFound}
	h := mcpServer(t, fb)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`)
	do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`)

	// A granted server that isn't in the host registry fails closed as 404.
	rr := do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"files","method":"tools/list"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("registry-miss: got %d, want 404 (%s)", rr.Code, rr.Body.String())
	}
}

func TestMCPCall_AppErrorIsOK(t *testing.T) {
	fb := &fakeBroker{result: mcpbroker.Result{Output: json.RawMessage(`{"isError":true}`), IsError: true}}
	h := mcpServer(t, fb)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`)
	do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`)

	rr := do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"files","method":"tools/call","params":{"name":"boom"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("app-error call: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	var resp MCPCallResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.OK || !resp.IsError {
		t.Errorf("want ok=false isError=true, got ok=%v isError=%v", resp.OK, resp.IsError)
	}
}

func TestMCPGrants_ListAndRevoke(t *testing.T) {
	fb := &fakeBroker{}
	h := mcpServer(t, fb)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`)

	// Empty by default (deny-all).
	rr := do(t, h, http.MethodGet, "/v1/islands/oc/mcp/grants", "")
	var gr MCPGrantsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &gr)
	if len(gr.Grants) != 0 {
		t.Fatalf("fresh island must have no grants, got %d", len(gr.Grants))
	}

	do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`)
	rr = do(t, h, http.MethodGet, "/v1/islands/oc/mcp/grants", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &gr)
	if len(gr.Grants) != 1 || gr.Grants[0].Server != "files" {
		t.Fatalf("want one grant 'files', got %+v", gr.Grants)
	}

	// A duplicate grant is a conflict.
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`); rr.Code != http.StatusConflict {
		t.Fatalf("duplicate grant: got %d, want 409", rr.Code)
	}
	// An invalid server name is a 400.
	if rr := do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"a/b"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad name: got %d, want 400", rr.Code)
	}

	// Revoke → gone → deny-all again.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/oc/mcp/grants/files", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: got %d, want 204 (%s)", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/v1/islands/oc/mcp/grants/files", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("revoke missing: got %d, want 404", rr.Code)
	}
}

// The full ledger story: grant, an allowed call, and a denied call are all
// recorded, and the hash chain verifies.
func TestMCPLedger(t *testing.T) {
	fb := &fakeBroker{result: mcpbroker.Result{Output: json.RawMessage(`{}`)}}
	h := mcpServer(t, fb)
	do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"oc","agent":"openclaw","role":"home"}`)
	do(t, h, http.MethodPost, "/v1/islands/oc/mcp/grants", `{"server":"files"}`)
	do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"files","method":"tools/call","params":{"name":"echo"}}`)
	do(t, h, http.MethodPost, "/v1/mcp/call", `{"island":"oc","server":"ghost","method":"tools/list"}`) // ungranted → deny

	rr := do(t, h, http.MethodGet, "/v1/audit", "")
	var ar AuditResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ar)
	var grant, call, deny bool
	for _, e := range ar.Entries {
		if e.Island != "oc" {
			continue
		}
		switch e.Type {
		case "mcp.grant":
			grant = grant || e.Scope == "files"
		case "mcp.call":
			call = call || e.Scope == "files"
		case "mcp.deny":
			deny = deny || e.Scope == "ghost"
		}
	}
	if !grant || !call || !deny {
		t.Errorf("ledger missing entries: grant=%v call=%v deny=%v", grant, call, deny)
	}
	if !ar.Verified {
		t.Errorf("ledger chain failed verify: %s", ar.Error)
	}
}

// Granting must never be reachable by an in-island bearer token — only the
// operator over the trusted control plane may open an MCP grant. (The brokered
// call route's in-island token reachability is a documented follow-up owned by
// tokenauth.go; granting must stay denied regardless.)
func TestMCPGrantsOperatorOnly(t *testing.T) {
	if err := authorizeToken("oc", "POST /v1/islands/{name}/mcp/grants", "/v1/islands/oc/mcp/grants"); err == nil {
		t.Fatal("granting an MCP server must be denied on the token path")
	}
	if err := authorizeToken("oc", "DELETE /v1/islands/{name}/mcp/grants/{server}", "/v1/islands/oc/mcp/grants/files"); err == nil {
		t.Fatal("revoking an MCP grant must be denied on the token path")
	}
}
