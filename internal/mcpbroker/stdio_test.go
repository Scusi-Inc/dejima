package mcpbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMockMCPServerProcess is not a real test: when GO_WANT_HELPER_PROCESS=1 it
// re-execs as a minimal stdio MCP server so the broker can be exercised against a
// real process speaking newline-delimited JSON-RPC. Its behavior is steered by
// the MOCK_MODE env the test injects via the registry spec's Env passthrough.
func TestMockMCPServerProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("MOCK_MODE")
	out := os.Stdout
	reply := func(v any) {
		line, _ := json.Marshal(v)
		fmt.Fprintf(out, "%s\n", line)
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			os.Exit(3)
		}
		switch msg.Method {
		case "initialize":
			// A server that dies mid-handshake — exit before replying.
			if mode == "close_init" {
				os.Exit(0)
			}
			reply(map[string]any{
				"jsonrpc": "2.0", "id": rawID(msg.ID),
				"result": map[string]any{
					"protocolVersion": protocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "mock", "version": "1"},
				},
			})
		case "notifications/initialized":
			// no reply
		case "tools/list":
			reply(map[string]any{
				"jsonrpc": "2.0", "id": rawID(msg.ID),
				"result": map[string]any{"tools": []map[string]any{{"name": "echo"}, {"name": "boom"}}},
			})
		case "tools/call":
			switch mode {
			case "close_call":
				os.Exit(0)
			case "hang":
				time.Sleep(60 * time.Second)
			case "rpc_error":
				reply(map[string]any{
					"jsonrpc": "2.0", "id": rawID(msg.ID),
					"error": map[string]any{"code": -32000, "message": "boom from server"},
				})
				continue
			}
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			isErr := p.Name == "boom"
			text := fmt.Sprintf("called %s args=%v env_island=%s", p.Name, p.Arguments, os.Getenv("DEJIMA_MCP_ISLAND"))
			reply(map[string]any{
				"jsonrpc": "2.0", "id": rawID(msg.ID),
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": text}},
					"isError": isErr,
				},
			})
		default:
			reply(map[string]any{
				"jsonrpc": "2.0", "id": rawID(msg.ID),
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
	os.Exit(0)
}

// rawID decodes a JSON-RPC id back to a value json.Marshal will re-emit as the
// same token (a number stays a number), so the broker's id match succeeds.
func rawID(raw json.RawMessage) any {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return string(raw)
}

// mockBroker builds a StdioBroker whose single registered server re-execs this
// test binary as the mock MCP server, with MOCK_MODE wired through the spec Env.
func mockBroker(t *testing.T, mode string) *StdioBroker {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	env := `["GO_WANT_HELPER_PROCESS=1"`
	if mode != "" {
		env += `, "MOCK_MODE=` + mode + `"`
	}
	env += `]`
	content := fmt.Sprintf(`[[servers]]
name = "mock"
transport = "stdio"
command = %q
args = ["-test.run=TestMockMCPServerProcess"]
env = %s
`, os.Args[0], env)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return &StdioBroker{Registry: &Registry{Path: path}, Timeout: 5 * time.Second}
}

func TestStdioBroker_ToolsCall(t *testing.T) {
	b := mockBroker(t, "")
	params := json.RawMessage(`{"name":"echo","arguments":{"msg":"hi"}}`)
	res, err := b.Call(context.Background(), Request{Island: "isle-1", Server: "mock", Method: "tools/call", Params: params})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected isError for echo: %s", res.Output)
	}
	if !strings.Contains(string(res.Output), "called echo") {
		t.Fatalf("result missing echo text: %s", res.Output)
	}
	// The island identity reached the server via the minimal env, not inheritance.
	if !strings.Contains(string(res.Output), "env_island=isle-1") {
		t.Fatalf("server did not see DEJIMA_MCP_ISLAND: %s", res.Output)
	}
}

func TestStdioBroker_ToolsList(t *testing.T) {
	b := mockBroker(t, "")
	res, err := b.Call(context.Background(), Request{Island: "i", Server: "mock", Method: "tools/list"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(string(res.Output), "echo") {
		t.Fatalf("tools/list missing echo: %s", res.Output)
	}
}

// A tools/call that completes with isError:true is a normal Result (the caller's
// concern), not a broker error — mirroring a capability's non-zero exit code.
func TestStdioBroker_ApplicationError(t *testing.T) {
	b := mockBroker(t, "")
	params := json.RawMessage(`{"name":"boom"}`)
	res, err := b.Call(context.Background(), Request{Island: "i", Server: "mock", Method: "tools/call", Params: params})
	if err != nil {
		t.Fatalf("Call should not error on isError result: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError true, got %s", res.Output)
	}
}

func TestStdioBroker_MethodNotAllowed(t *testing.T) {
	b := mockBroker(t, "")
	_, err := b.Call(context.Background(), Request{Island: "i", Server: "mock", Method: "initialize"})
	if !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("want ErrMethodNotAllowed, got %v", err)
	}
}

func TestStdioBroker_ServerNotFound(t *testing.T) {
	b := mockBroker(t, "")
	_, err := b.Call(context.Background(), Request{Island: "i", Server: "ghost", Method: "tools/list"})
	if !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("want ErrServerNotFound, got %v", err)
	}
}

func TestStdioBroker_RPCError(t *testing.T) {
	b := mockBroker(t, "rpc_error")
	_, err := b.Call(context.Background(), Request{Island: "i", Server: "mock", Method: "tools/call", Params: json.RawMessage(`{"name":"x"}`)})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol for a JSON-RPC error reply, got %v", err)
	}
}

func TestStdioBroker_ClosesDuringCall(t *testing.T) {
	b := mockBroker(t, "close_call")
	_, err := b.Call(context.Background(), Request{Island: "i", Server: "mock", Method: "tools/call", Params: json.RawMessage(`{"name":"x"}`)})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol when server closes mid-call, got %v", err)
	}
}

func TestStdioBroker_Timeout(t *testing.T) {
	b := mockBroker(t, "hang")
	b.Timeout = 400 * time.Millisecond
	start := time.Now()
	_, err := b.Call(context.Background(), Request{Island: "i", Server: "mock", Method: "tools/call", Params: json.RawMessage(`{"name":"x"}`)})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}
