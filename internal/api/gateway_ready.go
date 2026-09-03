package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
)

// Gateway readiness, daemon-side.
//
// This logic used to live only in cmd/dejima/agent_open.go, which meant any
// other client had to reimplement it and drift from us at the first change.
// Behind the proxy route it has one home both clients read.

// GatewayReadiness is one agent's console readiness.
//
// THREE STATES, not two, and the third is the reason State is a string rather
// than a bool: "the daemon could not ask" is not "nothing is listening". A
// consumer that renders the two the same tells an operator their gateway is
// down when nobody looked.
type GatewayReadiness struct {
	// State is "ready", "not-ready", or "unknown".
	State string `json:"state"`
	// Reason explains a non-ready state, for a surface to show verbatim.
	Reason string `json:"reason,omitempty"`
	// Port is the in-container gateway port (0 = this agent type has none).
	Port int `json:"port,omitempty"`
	// NeedsProviderKey is TRUE when this agent's framework reaches a model over
	// a provider key and none is configured.
	//
	// Deliberately separate from State and never folded into it. A keyless
	// gateway serves the readiness probe perfectly and then fails every task, so
	// "nothing is listening" and "listening but cannot reach a model" are
	// different problems with different remedies. Collapsing them produces a
	// signal that is right about the light and wrong about the cause — which is
	// the defect this week's work has been removing from three other surfaces.
	NeedsProviderKey bool `json:"needs_provider_key,omitempty"`
}

const (
	GatewayReady    = "ready"
	GatewayNotReady = "not-ready"
	GatewayUnknown  = "unknown"
)

// gatewayProbeBudget bounds one readiness probe. Short on purpose: this answers
// "is something serving right now", and a caller polling it wants an answer, not
// a wait. Waiting for a gateway to come up is the caller's loop to run, not
// ours to block in.
const gatewayProbeBudget = 3 * time.Second

// gatewayPortFor returns the agent's declared gateway port and whether its
// framework needs a provider key. Both are registry data, known before any
// connection exists.
func gatewayPortFor(a *project.AgentSpec) (port int, needsKey bool) {
	h, ok := handlers.Lookup(a.Type)
	if !ok {
		return 0, false
	}
	return h.GatewayPort, h.RequiresProviderKey
}

// gatewayReadiness probes the agent's gateway from inside the island.
//
// The probe is the same one `agent open` uses and for the same reason: send the
// least surprising HTTP request there is and require one byte back. Any HTTP
// server answers something — 200, 404, 401 — while a dial that connects and then
// dies gives EOF with nothing read. It deliberately does not ask for a health
// endpoint or interpret a status code. OpenClaw's own /ready returns 503 when a
// messaging channel is broken while the Control UI is perfectly usable, so
// wiring an indicator to it would ship a red light that does not mean what a
// reader takes it to mean.
func (s *Server) gatewayReadiness(ctx context.Context, p *project.Project, a *project.AgentSpec) GatewayReadiness {
	port, needsKey := gatewayPortFor(a)
	out := GatewayReadiness{Port: port}
	// The provider-key answer needs no connection and is reported whatever the
	// probe finds — including when the probe cannot run at all.
	if needsKey {
		provider, _, keySet, _ := agentProviderStatus(a)
		out.NeedsProviderKey = !keySet
		_ = provider
	}
	if port == 0 {
		out.State = GatewayNotReady
		out.Reason = "this agent type has no localhost gateway"
		return out
	}

	ctx, cancel := context.WithTimeout(ctx, gatewayProbeBudget)
	defer cancel()
	conn, err := s.rt.DialContainerPort(ctx, p.ContainerName(), "127.0.0.1", port)
	if err != nil {
		// A failed dial is genuinely "nothing is listening" only when we reached
		// the container to find that out. We cannot tell the two apart from here,
		// so this is the one place the distinction is lost — and it resolves to
		// unknown rather than not-ready, because claiming a gateway is down is a
		// stronger statement than admitting we could not check.
		out.State = GatewayUnknown
		out.Reason = "couldn't reach the island to probe its gateway: " + err.Error()
		return out
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(gatewayProbeBudget))
	if _, err := io.WriteString(conn, "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n"); err != nil {
		out.State = GatewayNotReady
		out.Reason = "nothing accepted a request on port " + strconv.Itoa(port)
		return out
	}
	var b [1]byte
	if n, err := conn.Read(b[:]); n > 0 && (err == nil || err == io.EOF) {
		out.State = GatewayReady
		return out
	}
	out.State = GatewayNotReady
	out.Reason = "nothing is serving on port " + strconv.Itoa(port) + " yet (starting, installing, or stopped)"
	return out
}

// getAgentGatewayReady answers "can I open this agent's console yet".
//
// A read, not an act: it opens a connection and closes it without sending work.
// Exposed so a browser client does not reimplement the probe and drift from the
// one `agent open` uses.
func (s *Server) getAgentGatewayReady(w http.ResponseWriter, r *http.Request) {
	name, agentID := r.PathValue("name"), r.PathValue("id")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	a := findAgent(p, agentID)
	if a == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, agentID))
		return
	}
	writeJSON(w, http.StatusOK, s.gatewayReadiness(r.Context(), p, a))
}
