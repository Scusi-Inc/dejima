package mcpbroker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// MCP stdio transport: newline-delimited JSON-RPC 2.0 messages, one per line, no
// embedded newlines. The broker runs the server as a one-shot child: it performs
// the lifecycle handshake (initialize → initialized), issues exactly one brokered
// call, then closes stdin so the server exits. Everything is bounded by the
// caller's context and an output cap so a misbehaving server can't hang or flood
// the daemon.

const (
	// protocolVersion is the MCP revision the broker advertises in initialize.
	// Servers negotiate down if they only speak an older one.
	protocolVersion = "2024-11-05"
	// stdioWaitDelay bounds how long Wait blocks for I/O to drain after the
	// process is signalled (e.g. on a context timeout) before the pipes are
	// force-closed — so a child holding a pipe can't keep the broker hostage.
	stdioWaitDelay = 1 * time.Second
)

// rpcRequest is an outbound JSON-RPC request or notification. A notification
// omits ID (Notif=true); a request carries a positive ID the response echoes.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcMessage is any inbound line. A response carries ID + (Result|Error) and no
// Method; a server-initiated request/notification carries Method — those we skip,
// since the broker only awaits its own responses.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// callStdio runs one brokered JSON-RPC call against a stdio MCP server and
// returns the raw `result` of the call. It owns the full lifecycle and never
// lets request data reach a shell: the server program is exec'd with a fixed
// argv and a minimal, explicit environment.
func callStdio(ctx context.Context, spec ServerSpec, req Request, maxOutput int64) (Result, error) {
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.WaitDelay = stdioWaitDelay
	cmd.Env = brokerEnv(spec, req)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("%w: stdin pipe: %v", ErrProtocol, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("%w: stdout pipe: %v", ErrProtocol, err)
	}
	// Capture stderr (bounded) purely to enrich an error if the server dies.
	stderr := &limitWriter{n: 4 << 10}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("%w: start %q: %v", ErrProtocol, spec.Command, err)
	}
	// Always reap the process. Closing stdin (below) prompts a well-behaved server
	// to exit; the context + WaitDelay backstop one that doesn't.
	defer func() { _ = cmd.Wait() }()
	defer func() { _ = stdin.Close() }()

	msgs := make(chan rpcMessage)
	scanErr := make(chan error, 1)
	go scanMessages(stdout, maxOutput, msgs, scanErr)

	send := func(r rpcRequest) error {
		r.JSONRPC = "2.0"
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := stdin.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("%w: write %s: %v", ErrProtocol, r.Method, err)
		}
		return nil
	}

	// 1) initialize handshake.
	initParams, _ := json.Marshal(map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "dejima-mcpbroker", "version": "0.1"},
	})
	if err := send(rpcRequest{ID: 1, Method: "initialize", Params: initParams}); err != nil {
		return Result{}, err
	}
	if _, err := awaitResponse(ctx, 1, msgs, scanErr, stderr); err != nil {
		return Result{}, err
	}
	// 2) initialized notification (no id — fire and forget).
	if err := send(rpcRequest{Method: "notifications/initialized"}); err != nil {
		return Result{}, err
	}
	// 3) the one brokered call.
	if err := send(rpcRequest{ID: 2, Method: req.Method, Params: req.Params}); err != nil {
		return Result{}, err
	}
	msg, err := awaitResponse(ctx, 2, msgs, scanErr, stderr)
	if err != nil {
		return Result{}, err
	}
	return Result{Output: msg.Result, IsError: resultIsError(msg.Result)}, nil
}

// awaitResponse reads inbound lines until the response whose id matches wantID
// arrives, skipping server-initiated requests/notifications. It maps a context
// deadline to ErrTimeout, an early EOF to ErrProtocol (with any stderr tail), and
// a JSON-RPC error reply to ErrProtocol — a brokered/lifecycle request that
// errors is a protocol failure, distinct from a tools/call that *completes* with
// isError:true (that is a normal Result).
func awaitResponse(ctx context.Context, wantID int, msgs <-chan rpcMessage, scanErr <-chan error, stderr *limitWriter) (rpcMessage, error) {
	want := []byte(fmt.Sprintf("%d", wantID))
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return rpcMessage{}, ErrTimeout
			}
			return rpcMessage{}, fmt.Errorf("%w: %v", ErrProtocol, ctx.Err())
		case err := <-scanErr:
			if err != nil {
				return rpcMessage{}, fmt.Errorf("%w: reading server output: %v", ErrProtocol, err)
			}
			return rpcMessage{}, serverClosedErr(stderr)
		case msg, ok := <-msgs:
			if !ok {
				return rpcMessage{}, serverClosedErr(stderr)
			}
			if msg.Method != "" || len(msg.ID) == 0 || string(msg.ID) == "null" {
				continue // a server-initiated request/notification — not our reply
			}
			if !bytes.Equal(bytes.TrimSpace(msg.ID), want) {
				continue // a stray/duplicate id — keep waiting for ours
			}
			if msg.Error != nil {
				return rpcMessage{}, fmt.Errorf("%w: %v", ErrProtocol, msg.Error)
			}
			return msg, nil
		}
	}
}

func serverClosedErr(stderr *limitWriter) error {
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%w: server closed without responding: %s", ErrProtocol, tail)
	}
	return fmt.Errorf("%w: server closed without responding", ErrProtocol)
}

// scanMessages decodes newline-delimited JSON-RPC from r onto msgs until EOF or
// the output cap, then reports the terminal error (nil on clean EOF) on scanErr
// and closes msgs. The scanner buffer is raised to maxOutput so a large (but
// bounded) tools/call result isn't truncated mid-line.
func scanMessages(r io.Reader, maxOutput int64, msgs chan<- rpcMessage, scanErr chan<- error) {
	defer close(msgs)
	sc := bufio.NewScanner(r)
	cap := int(maxOutput)
	if cap < 64<<10 {
		cap = 64 << 10
	}
	sc.Buffer(make([]byte, 0, 64<<10), cap)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			scanErr <- fmt.Errorf("malformed json-rpc line: %w", err)
			return
		}
		msgs <- msg
	}
	scanErr <- sc.Err()
}

// resultIsError reports whether a tools/call result object carried isError:true.
// Other methods have no such field, so this is false for them.
func resultIsError(result json.RawMessage) bool {
	if len(result) == 0 {
		return false
	}
	var v struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(result, &v)
	return v.IsError
}

// brokerEnv is the minimal, explicit environment for the server process: the
// island identity, a fixed PATH, and the operator-declared passthrough Env from
// the registry. The daemon's own environment is never inherited.
func brokerEnv(spec ServerSpec, req Request) []string {
	env := []string{
		"DEJIMA_MCP_ISLAND=" + req.Island,
		"DEJIMA_MCP_AGENT=" + req.Agent,
		"DEJIMA_MCP_SERVER=" + req.Server,
		"PATH=/usr/bin:/bin:/usr/local/bin",
	}
	for _, kv := range spec.Env {
		if strings.Contains(kv, "=") {
			env = append(env, kv)
		}
	}
	return env
}

// limitWriter captures at most n bytes, discarding the rest. Used for the
// bounded stderr tail.
type limitWriter struct {
	buf bytes.Buffer
	n   int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if int64(len(p)) > l.n {
		l.buf.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= int64(len(p))
	return l.buf.Write(p)
}

func (l *limitWriter) String() string { return l.buf.String() }
