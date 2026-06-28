package project

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel error kinds for agent-ref resolution, so callers can branch on the
// failure mode (e.g. the mailbox treats "no match" as a permissive pass-through
// but rejects "ambiguous"). Wrapped errors carry the human-readable detail.
var (
	// ErrNoSuchAgent: the ref matched neither an id nor any label.
	ErrNoSuchAgent = errors.New("no such agent")
	// ErrAmbiguousAgent: the ref matched more than one agent's label.
	ErrAmbiguousAgent = errors.New("ambiguous agent")
)

// AgentRef is the minimal (id, label) view the agent-ref resolver needs. Both an
// AgentSpec (daemon-side) and the CLI's api.AgentInfo satisfy it, so ONE resolver
// backs every "address an agent by name" path. The resolver never reads anything
// else off an agent, keeping it usable from either trust domain.
type AgentRef interface {
	RefID() string
	RefLabel() string
}

// RefID / RefLabel let an AgentSpec be used directly as an AgentRef.
func (a AgentSpec) RefID() string    { return a.ID }
func (a AgentSpec) RefLabel() string { return a.Label }

// ResolveAgentRef maps a user-supplied agent reference to a concrete agent id,
// over an arbitrary set of agents. Resolution order (id always wins, for
// back-compat — every place that took an id keeps working):
//
//  1. exact id match → that id.
//  2. else case-insensitive label match → the matched agent's id.
//  3. multiple label matches → an "ambiguous" error listing each id(label),
//     directing the user to the id.
//  4. no match → a "no such agent" error.
//
// ref is trimmed of surrounding whitespace first. An empty ref returns an error
// (callers that mean "the primary" should not route through here).
func ResolveAgentRef[T AgentRef](agents []T, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("no agent reference given")
	}
	// 1. Exact id wins — ids are the durable handle and must never be shadowed by
	// a (cosmetic, renamable) label.
	for _, a := range agents {
		if a.RefID() == ref {
			return ref, nil
		}
	}
	// 2/3. Case-insensitive label match; collect all to disambiguate.
	var matches []T
	for _, a := range agents {
		if a.RefLabel() != "" && strings.EqualFold(a.RefLabel(), ref) {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].RefID(), nil
	case 0:
		return "", fmt.Errorf("%w %q", ErrNoSuchAgent, ref)
	default:
		parts := make([]string, 0, len(matches))
		for _, m := range matches {
			parts = append(parts, fmt.Sprintf("%s(%s)", m.RefID(), m.RefLabel()))
		}
		return "", fmt.Errorf("%w %q: %s — use the id", ErrAmbiguousAgent, ref, strings.Join(parts, ", "))
	}
}

// ResolveAgentRef resolves a user-supplied ref (an id or a label) against this
// island's agents and returns the matching AgentSpec. See the package-level
// ResolveAgentRef for the resolution rules (id-wins, case-insensitive label,
// ambiguity and no-match errors).
func (p *Project) ResolveAgentRef(ref string) (*AgentSpec, error) {
	id, err := ResolveAgentRef(p.Agents, ref)
	if err != nil {
		return nil, err
	}
	a, ok := p.AgentByID(id)
	if !ok {
		// Unreachable: the resolver only returns ids it found among p.Agents.
		return nil, fmt.Errorf("no such agent %q", ref)
	}
	return a, nil
}
