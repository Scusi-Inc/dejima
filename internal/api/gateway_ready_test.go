package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
)

// Readiness has THREE states because two would lie. "nothing is listening" and
// "the daemon could not ask" are different facts with different remedies, and a
// surface that renders them the same tells an operator their gateway is down
// when nobody looked.
//
// The provider-key answer is a fourth, orthogonal thing: a keyless gateway
// serves this probe perfectly and then fails every task.

func readinessFor(t *testing.T, agentType string, dial func(context.Context, string, string, int) (net.Conn, error)) GatewayReadiness {
	t.Helper()
	h, f := newTestServer(t)
	f.dialFn = dial
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"name":"alpha","repo":"https://github.com/o/r","agent":"`+agentType+`"}`); rr.Code >= 300 {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}
	var isl IslandInfo
	if err := json.Unmarshal(do(t, h, http.MethodGet, "/v1/islands/alpha", "").Body.Bytes(), &isl); err != nil {
		t.Fatal(err)
	}
	rr := do(t, h, http.MethodGet, "/v1/islands/alpha/agents/"+isl.Agents[0].ID+"/gateway-ready", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("gateway-ready: %d %s", rr.Code, rr.Body.String())
	}
	var out GatewayReadiness
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// servingConn is a gateway that answers, i.e. ready.
func servingConn(t *testing.T) func(context.Context, string, string, int) (net.Conn, error) {
	t.Helper()
	return func(context.Context, string, string, int) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buf := make([]byte, 256)
			if _, err := server.Read(buf); err != nil {
				return
			}
			_, _ = io.WriteString(server, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n")
		}()
		return client, nil
	}
}

// silentConn accepts and closes without writing — the shape of a tunnel whose
// far end is not there. This is the state a bare dial cannot distinguish from a
// working gateway.
func silentConn() func(context.Context, string, string, int) (net.Conn, error) {
	return func(context.Context, string, string, int) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			buf := make([]byte, 256)
			_, _ = server.Read(buf)
			server.Close()
		}()
		return client, nil
	}
}

func TestGatewayReadyWhenSomethingAnswers(t *testing.T) {
	got := readinessFor(t, "openclaw", servingConn(t))
	if got.State != GatewayReady {
		t.Errorf("state = %q (%s), want %q", got.State, got.Reason, GatewayReady)
	}
	if got.Port == 0 {
		t.Error("readiness should report the port it probed")
	}
}

// A 401 counts as ready and THIS must not, or the distinction is worthless.
func TestGatewayNotReadyWhenNothingServes(t *testing.T) {
	got := readinessFor(t, "openclaw", silentConn())
	if got.State != GatewayNotReady {
		t.Errorf("state = %q, want %q — accept-then-close is not a gateway", got.State, GatewayNotReady)
	}
	if got.Reason == "" {
		t.Error("a non-ready state must carry a reason a surface can show verbatim")
	}
}

// The third state, and the reason State is not a bool. Failing to reach the
// island is not evidence about what is listening inside it.
func TestGatewayUnknownWhenTheIslandCannotBeReached(t *testing.T) {
	got := readinessFor(t, "openclaw", func(context.Context, string, string, int) (net.Conn, error) {
		return nil, errors.New("container is not running")
	})
	if got.State != GatewayUnknown {
		t.Errorf("state = %q, want %q — an unreachable island is not a down gateway", got.State, GatewayUnknown)
	}
}

// The three states must stay three. Each test above passing alone would not
// catch two of them collapsing into one.
func TestGatewayReadinessStatesStayDistinct(t *testing.T) {
	seen := map[string]string{}
	for label, dial := range map[string]func(context.Context, string, string, int) (net.Conn, error){
		"serving":     servingConn(t),
		"silent":      silentConn(),
		"unreachable": func(context.Context, string, string, int) (net.Conn, error) { return nil, errors.New("x") },
	} {
		st := readinessFor(t, "openclaw", dial).State
		if prev, dup := seen[st]; dup {
			t.Errorf("%q and %q both produce state %q — the probe no longer separates them", prev, label, st)
		}
		seen[st] = label
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct states, got %d: %v", len(seen), seen)
	}
}

// The provider-key answer is orthogonal and must survive a READY gateway: that
// is the whole point. An openclaw agent with no key configured serves the probe
// and fails every task.
func TestGatewayReadinessReportsAMissingProviderKeyEvenWhenReady(t *testing.T) {
	got := readinessFor(t, "openclaw", servingConn(t))
	if got.State != GatewayReady {
		t.Fatalf("precondition: state = %q, want ready", got.State)
	}
	if !got.NeedsProviderKey {
		t.Error("an openclaw agent with no provider key must be flagged even when the gateway answers — " +
			"a keyless gateway serves this probe perfectly and then fails every task")
	}
}

// ...and must NOT be flagged for a framework the provider subsystem doesn't
// apply to. A warning that fires on everything is one nobody reads.
func TestGatewayReadinessQuietForFrameworksNeedingNoKey(t *testing.T) {
	got := readinessFor(t, "claude-code", silentConn())
	if got.NeedsProviderKey {
		t.Error("claude-code is OAuth-seeded; it must not be reported as needing a provider key")
	}
}
