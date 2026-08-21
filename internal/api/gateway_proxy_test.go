package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The gateway proxy relays bytes to a framework console inside an island.
//
// THE TEST THAT MATTERS IS THE UPGRADE ONE. A ReverseProxy over a ResponseWriter
// that cannot Hijack serves the page perfectly and silently fails the websocket:
// HTTP works, the live connection does not, and the symptom is indistinguishable
// from the framework being broken. This tree has shipped that misattribution
// once already. A test proving a GET returns 200 proves the half that was never
// in doubt.

// fakeGateway is an in-process stand-in for the framework's console. It records
// what the proxy sent it and can answer either as plain HTTP or by completing a
// raw protocol upgrade.
type fakeGateway struct {
	ln       net.Listener
	mu       chan struct{} // 1-buffered, guards the fields below
	lastPath string
	lastAuth string
	upgraded bool
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGateway{ln: ln, mu: make(chan struct{}, 1)}
	g.mu <- struct{}{}
	t.Cleanup(func() { _ = ln.Close() })
	go g.serve()
	return g
}

func (g *fakeGateway) with(fn func()) {
	<-g.mu
	fn()
	g.mu <- struct{}{}
}

func (g *fakeGateway) serve() {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.handle(c)
	}
}

func (g *fakeGateway) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	g.with(func() {
		g.lastPath = req.URL.Path
		g.lastAuth = req.Header.Get("Authorization")
	})
	if isUpgradeRequest(req) {
		g.with(func() { g.upgraded = true })
		// Complete the upgrade, then echo — proving the connection is genuinely
		// two-way after the switch rather than merely reporting 101.
		_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf := make([]byte, 64)
		n, rerr := br.Read(buf)
		if rerr == nil && n > 0 {
			_, _ = c.Write(append([]byte("echo:"), buf[:n]...))
		}
		return
	}
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nContent-Type: text/plain\r\n\r\nhello")
}

// gatewayEnv wires a server whose island dials the fake gateway instead of a
// container, seeds an island with an openclaw agent, and returns an httptest
// server (NOT a bare handler — the proxy needs a ResponseWriter that can Hijack,
// which is exactly what a recorder cannot do).
func gatewayEnv(t *testing.T) (*httptest.Server, *fakeGateway) {
	t.Helper()
	h, f := newTestServer(t)
	g := newFakeGateway(t)
	f.dialFn = func(ctx context.Context, _, _ string, _ int) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", g.ln.Addr().String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"name":"alpha","repo":"https://github.com/o/r","agent":"openclaw"}`); rr.Code >= 300 {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, g
}

// agentIDOf returns the island's first agent id.
func agentIDOf(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/v1/islands/alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out IslandInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Agents) == 0 {
		t.Fatal("island has no agents")
	}
	return out.Agents[0].ID
}

func TestGatewayProxyRelaysAPlainRequest(t *testing.T) {
	ts, g := gatewayEnv(t)
	id := agentIDOf(t, ts)

	resp, err := http.Get(ts.URL + "/v1/islands/alpha/agents/" + id + "/gateway/some/page?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("status %d body %q, want 200 hello", resp.StatusCode, body)
	}
	// The gateway must see the path BELOW /gateway, not our route's path.
	g.with(func() {
		if g.lastPath != "/some/page" {
			t.Errorf("upstream path = %q, want /some/page — the client's path must be "+
				"rewritten, or every gateway route 404s", g.lastPath)
		}
	})
}

// THE ONE THAT MATTERS. A real upgrade, completed, with bytes flowing after the
// switch. Asserting only the 101 would pass against a proxy that reports the
// status and then drops the connection — which is the exact failure this test
// exists for.
func TestGatewayProxyCompletesAWebsocketUpgrade(t *testing.T) {
	ts, g := gatewayEnv(t)
	id := agentIDOf(t, ts)

	u := strings.TrimPrefix(ts.URL, "http://")
	c, err := net.DialTimeout("tcp", u, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))

	req := "GET /v1/islands/alpha/agents/" + id + "/gateway/ws HTTP/1.1\r\n" +
		"Host: " + u + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: keep-alive, Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the upgrade response: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status = %q, want 101 Switching Protocols", strings.TrimSpace(status))
	}
	// Drain headers.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	// The half that a 101-only test would miss: the tunnel must carry bytes in
	// both directions after the switch.
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("writing after upgrade: %v", err)
	}
	// ReadFull, not Read: this is a byte stream, and a single Read is entitled to
	// return a partial. The first version of this test used Read, passed here,
	// and failed on CI with "echo:" — a test that is flaky about stream
	// boundaries, written while documenting flaky tests. The assertion is about
	// what arrives, not how it is chunked, so it must not depend on chunking.
	want := "echo:ping"
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("the connection did not survive the upgrade — this is the silent "+
			"websocket failure the proxy exists to avoid: %v", err)
	}
	if got := string(buf); got != want {
		t.Errorf("read %q after upgrade, want %q", got, want)
	}
	g.with(func() {
		if !g.upgraded {
			t.Error("the gateway never saw an upgrade request")
		}
	})
}

// The control on the test above. It only means something if a NON-hijackable
// ResponseWriter actually breaks the upgrade — if httptest.NewRecorder could
// carry it too, the test would pass against a proxy with no upgrade support and
// prove nothing.
func TestRecorderCannotCarryAnUpgrade(t *testing.T) {
	var w http.ResponseWriter = httptest.NewRecorder()
	if _, ok := w.(http.Hijacker); ok {
		t.Fatal("httptest.Recorder now supports Hijack — the upgrade test above no longer " +
			"proves the middleware chain forwards it, because a broken chain would pass too")
	}
}

// The pinned console token is injected server-side. If it ever reached the
// client instead, a credential would be sitting in a browser — the thing
// proxying was chosen to avoid.
func TestGatewayProxyInjectsTheTokenUpstreamOnly(t *testing.T) {
	ts, g := gatewayEnv(t)
	id := agentIDOf(t, ts)

	resp, err := http.Get(ts.URL + "/v1/islands/alpha/agents/" + id + "/gateway/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// The fake runtime's Exec returns no token, so the header must be absent
	// rather than present-and-empty: a literal "Bearer " would authenticate as
	// nobody and read as a gateway bug.
	g.with(func() {
		if strings.TrimSpace(g.lastAuth) == "Bearer" {
			t.Error("sent an empty bearer token upstream; omit the header when there is no token")
		}
	})
	for _, h := range []string{"Authorization", "Set-Cookie"} {
		if v := resp.Header.Get(h); strings.Contains(strings.ToLower(v), "bearer") {
			t.Errorf("response carries %s=%q — the gateway credential must never reach the client", h, v)
		}
	}
}

func TestGatewayProxyRejectsAnAgentWithNoGateway(t *testing.T) {
	h, f := newTestServer(t)
	f.dialFn = func(context.Context, string, string, int) (net.Conn, error) {
		return nil, errors.New("should not dial")
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"name":"beta","repo":"https://github.com/o/r","agent":"claude-code"}`); rr.Code >= 300 {
		t.Fatalf("create island: %d %s", rr.Code, rr.Body.String())
	}
	rr := do(t, h, http.MethodGet, "/v1/islands/beta", "")
	var isl IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &isl); err != nil {
		t.Fatal(err)
	}
	got := do(t, h, http.MethodGet, "/v1/islands/beta/agents/"+isl.Agents[0].ID+"/gateway/", "")
	if got.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an agent type with no gateway", got.Code)
	}
}

func TestParseGatewayPath(t *testing.T) {
	for _, c := range []struct {
		in                  string
		island, agent, rest string
		ok                  bool
	}{
		{"/v1/islands/a/agents/a1/gateway/", "a", "a1", "/", true},
		{"/v1/islands/a/agents/a1/gateway/x/y", "a", "a1", "/x/y", true},
		{"/v1/islands/a/agents/a1/gateway", "a", "a1", "/", true},
		{"/v1/islands/a/agents/a1", "", "", "", false},
		{"/v1/islands/a/gateway/x", "", "", "", false},
		{"/v2/islands/a/agents/a1/gateway/x", "", "", "", false},
	} {
		island, agent, rest, ok := parseGatewayPath(c.in)
		if ok != c.ok || island != c.island || agent != c.agent || rest != c.rest {
			t.Errorf("parseGatewayPath(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, island, agent, rest, ok, c.island, c.agent, c.rest, c.ok)
		}
	}
}

func TestIsUpgradeRequest(t *testing.T) {
	for _, c := range []struct {
		upgrade, connection string
		want                bool
	}{
		{"websocket", "Upgrade", true},
		{"WebSocket", "keep-alive, Upgrade", true}, // both are case-insensitive; comma lists are legal
		{"websocket", "keep-alive", false},
		{"", "Upgrade", false},
		{"h2c", "Upgrade", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.upgrade != "" {
			r.Header.Set("Upgrade", c.upgrade)
		}
		r.Header.Set("Connection", c.connection)
		if got := isUpgradeRequest(r); got != c.want {
			t.Errorf("Upgrade=%q Connection=%q → %v, want %v", c.upgrade, c.connection, got, c.want)
		}
	}
}
