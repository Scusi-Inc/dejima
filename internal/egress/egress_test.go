package egress

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// denyHost is a Policy that denies one specific host (for the deny-path test).
type denyHost struct{ host string }

func (d denyHost) Allow(_, host string) bool { return host != d.host }

func proxyClient(t *testing.T, proxyURL, island string) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(island, "tok") // island travels as the proxy username
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
}

func TestProxyHTTPForwardsAndRecords(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "hello from backend")
	}))
	defer backend.Close()

	log := NewLog(16)
	px := httptest.NewServer(NewProxy(log, AllowAll{}))
	defer px.Close()

	resp, err := proxyClient(t, px.URL, "alpha").Get(backend.URL)
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from backend" {
		t.Fatalf("body = %q, want backend response (forwarding broken)", body)
	}

	ev := log.List("alpha")
	if len(ev) != 1 {
		t.Fatalf("want 1 recorded event, got %d", len(ev))
	}
	beHost, bePort := hostPort(backend.Listener.Addr().String(), "80")
	if ev[0].Host != beHost || ev[0].Port != bePort {
		t.Errorf("recorded %s:%s, want %s:%s", ev[0].Host, ev[0].Port, beHost, bePort)
	}
	if ev[0].Decision != DecisionAllow || ev[0].Method != http.MethodGet {
		t.Errorf("event = %+v, want allow/GET", ev[0])
	}
}

func TestProxyDenyBlocksAndRecords(t *testing.T) {
	hit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		fmt.Fprint(w, "should not reach")
	}))
	defer backend.Close()
	beHost, _ := hostPort(backend.Listener.Addr().String(), "80")

	log := NewLog(16)
	px := httptest.NewServer(NewProxy(log, denyHost{host: beHost}))
	defer px.Close()

	resp, err := proxyClient(t, px.URL, "beta").Get(backend.URL)
	if err != nil {
		t.Fatalf("request error (expected a 403, not a transport error): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if hit {
		t.Error("denied request still reached the backend")
	}
	ev := log.List("beta")
	if len(ev) != 1 || ev[0].Decision != DecisionDeny {
		t.Fatalf("want 1 deny event, got %+v", ev)
	}
}

func TestProxyAttributesIslandFromAuth(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer backend.Close()
	log := NewLog(16)
	px := httptest.NewServer(NewProxy(log, AllowAll{}))
	defer px.Close()

	if _, err := proxyClient(t, px.URL, "gamma").Get(backend.URL); err != nil {
		t.Fatal(err)
	}
	if got := log.List("gamma"); len(got) != 1 {
		t.Fatalf("event not attributed to gamma (got %d under gamma)", len(got))
	}
	if got := log.List("alpha"); len(got) != 0 {
		t.Errorf("event leaked to the wrong island")
	}
}

func TestProxyConnectTunnels(t *testing.T) {
	// A raw TCP echo server stands in for an HTTPS origin; CONNECT just tunnels.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c) // echo
	}()

	log := NewLog(16)
	px := httptest.NewServer(NewProxy(log, AllowAll{}))
	defer px.Close()

	// Speak CONNECT to the proxy manually.
	pxHost := px.Listener.Addr().String()
	conn, err := net.Dial("tcp", pxHost)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
		ln.Addr().String(), ln.Addr().String(), basicAuth("delta", "tok"))
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		t.Fatalf("CONNECT response = %q err=%v, want 200", status, err)
	}
	// Drain the blank line terminating the response headers.
	for {
		line, _ := br.ReadString('\n')
		if line == "\r\n" || line == "\n" || line == "" {
			break
		}
	}
	// Now the tunnel is open to the echo server.
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("tunnel read: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("tunnel echo = %q, want ping", buf)
	}

	ev := log.List("delta")
	if len(ev) != 1 || ev[0].Method != http.MethodConnect || ev[0].Decision != DecisionAllow {
		t.Fatalf("CONNECT event = %+v, want 1 allow/CONNECT", ev)
	}
}

func TestLogRingBoundsPerIsland(t *testing.T) {
	log := NewLog(3)
	for i := 0; i < 5; i++ {
		log.Record(Event{Island: "x", Host: fmt.Sprintf("h%d", i)})
	}
	got := log.List("x")
	if len(got) != 3 {
		t.Fatalf("ring not bounded: len=%d, want 3", len(got))
	}
	if got[0].Host != "h2" || got[2].Host != "h4" {
		t.Errorf("ring kept wrong window: %v", []string{got[0].Host, got[2].Host})
	}
	// Empty-island events are dropped.
	log.Record(Event{Island: "", Host: "nope"})
	if len(log.List("")) != 0 {
		t.Error("unattributed event should be dropped")
	}
}

func TestIslandFromAuth(t *testing.T) {
	if got := islandFromAuth("Basic " + basicAuth("alpha", "tok")); got != "alpha" {
		t.Errorf("got %q, want alpha", got)
	}
	if got := islandFromAuth(""); got != "" {
		t.Errorf("empty header should yield empty island, got %q", got)
	}
	if got := islandFromAuth("Bearer xyz"); got != "" {
		t.Errorf("non-Basic should yield empty island, got %q", got)
	}
}
