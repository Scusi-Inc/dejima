package egress

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Proxy is the island egress forward proxy: an http.Handler that serves both
// plain-HTTP proxy requests (absolute-URI form) and HTTPS CONNECT tunnels. For
// each connection it attributes the island (from Proxy-Authorization), asks the
// Policy, records an Event, and — if allowed — forwards the traffic. It is the
// daemon-run chokepoint islands are pointed at via HTTPS_PROXY.
//
// It deliberately does NOT terminate TLS: HTTPS is a blind CONNECT tunnel, so
// the proxy sees the destination host but never the encrypted payload. Egress
// control here is "who can it talk to," not content inspection.
type Proxy struct {
	rec    Recorder
	pol    Policy
	dialer *net.Dialer
	tr     *http.Transport
	now    func() time.Time // injectable for tests
}

// NewProxy builds a proxy recording to rec and gated by pol. Nil rec/pol default
// to a discard recorder and AllowAll (the observe-first posture), so a
// zero-config proxy is safe and never nil-panics.
func NewProxy(rec Recorder, pol Policy) *Proxy {
	if rec == nil {
		rec = discard{}
	}
	if pol == nil {
		pol = AllowAll{}
	}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &Proxy{
		rec:    rec,
		pol:    pol,
		dialer: d,
		tr:     &http.Transport{DialContext: d.DialContext},
		now:    time.Now,
	}
}

// islandFromAuth extracts the island name from a Proxy-Authorization: Basic
// header — the username field. The daemon injects HTTPS_PROXY with the island as
// the proxy username, so attribution travels with each request (no source-IP
// registry, no per-island listener). Returns "" when absent/malformed.
func islandFromAuth(h string) string {
	const p = "Basic "
	if !strings.HasPrefix(h, p) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(p):]))
	if err != nil {
		return ""
	}
	user, _, _ := strings.Cut(string(raw), ":")
	return user
}

// hostPort splits a "host:port" authority, defaulting the port when absent.
func hostPort(authority, defPort string) (host, port string) {
	h, p, err := net.SplitHostPort(authority)
	if err != nil {
		return authority, defPort
	}
	return h, p
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	island := islandFromAuth(r.Header.Get("Proxy-Authorization"))
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r, island)
		return
	}
	p.handleHTTP(w, r, island)
}

// handleConnect serves an HTTPS tunnel: record the decision, and on allow hijack
// the client conn, dial the target, and copy bytes both ways until either side
// closes.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request, island string) {
	host, port := hostPort(r.Host, "443")
	allowed := p.record(island, host, port, http.MethodConnect)
	if !allowed {
		http.Error(w, "egress denied by policy", http.StatusForbidden)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy: hijacking unsupported", http.StatusInternalServerError)
		return
	}
	target, err := p.dialer.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		http.Error(w, "egress dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = target.Close()
		return
	}
	defer client.Close()
	defer target.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	// Bidirectional copy; either direction ending tears down the tunnel.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
	<-done
}

// handleHTTP serves a plain-HTTP proxy request (absolute-URI form): record, and
// on allow forward via the shared transport and stream the response back.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request, island string) {
	if !r.URL.IsAbs() {
		http.Error(w, "proxy: expected an absolute request URI", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	if !p.record(island, host, port, r.Method) {
		http.Error(w, "egress denied by policy", http.StatusForbidden)
		return
	}

	// Forward: strip proxy-only/hop-by-hop headers and let the transport dial.
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.Header.Del("Proxy-Authorization")
	outReq.Header.Del("Proxy-Connection")
	resp, err := p.tr.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "egress request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// record applies the policy and records the event, returning whether the
// connection is allowed. Centralized so HTTP and CONNECT log identically.
func (p *Proxy) record(island, host, port, method string) bool {
	allowed := p.pol.Allow(island, host)
	d := DecisionAllow
	if !allowed {
		d = DecisionDeny
	}
	p.rec.Record(Event{
		Island: island, Host: host, Port: port, Method: method,
		Decision: d, Time: p.now().UTC(),
	})
	return allowed
}

// ensure the proxy is an http.Handler.
var _ http.Handler = (*Proxy)(nil)
