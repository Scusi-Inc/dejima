package api

import "net/http"

// The observed-agents endpoint: agents Dejima can SEE and cannot STOP.
//
// This is deliberately a SEPARATE COLLECTION from an island's agent list rather
// than a flag on it. An observed agent has no island, so it cannot be nested
// under one — and that structural fact is what keeps every island-keyed surface
// unreachable from it. Nobody has to remember not to call the grants pane with
// an observed agent; there is no island name to call it with.

// handleObservedAgents lists agents Dejima observes but does not gate.
//
// It returns an EMPTY LIST WITH registered:false today, and that pair is the
// honest answer rather than a placeholder. There is no registration path yet, so
// "no observed agents exist" and "Dejima has no way to learn about one" are
// different statements — and a client rendering an empty section for the second
// is claiming Dejima looked and found nothing. The flag lets the surface say
// which is true instead of guessing.
func (s *Server) handleObservedAgents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, ObservedAgentsResponse{
		// Non-nil so the field marshals as [] rather than null: a client that
		// distinguishes them will, and "null" is a third state nobody decided on.
		Agents: []AgentInfo{},
		// Flips to true when a registration path ships. Until then the surface
		// must not present emptiness as a finding.
		Registered: false,
	})
}

// stampObserved sets the containment level on agents leaving this collection.
//
// The level is decided HERE, by which collection an agent came from, exactly as
// agentInfos decides `contained` by an agent being in an island. Neither reads a
// level off a stored record: containment is encoded in both a field and a
// location, so if the record could win they would eventually disagree and every
// consumer would pick a different one to trust.
//
// Unused while the list is empty. It exists now because the alternative is that
// whoever adds registration also has to notice the stamping rule, and the two
// stamping sites should be findable together.
func stampObserved(agents []AgentInfo) []AgentInfo {
	out := make([]AgentInfo, len(agents))
	for i, a := range agents {
		a.Containment = ContainmentObserved
		out[i] = a
	}
	return out
}
