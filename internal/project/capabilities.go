package project

import (
	"fmt"
	"strings"
	"time"
)

// CapabilityGrant is an island's permission to invoke one named host capability
// target — a macOS Shortcut, or an executable in ~/.dejima/capabilities/ on
// Linux. Deny-all is the default: an island may invoke only the targets granted
// here. The adapter that runs a target is chosen by host OS at execution time,
// not stored per grant. See docs/capability-broker-spec.md.
type CapabilityGrant struct {
	Target    string    `toml:"target"`
	GrantedAt time.Time `toml:"granted_at"`
}

// CapabilityGrantByTarget returns the grant for target, or ok=false.
func (p *Project) CapabilityGrantByTarget(target string) (CapabilityGrant, bool) {
	for _, c := range p.Capabilities {
		if c.Target == target {
			return c, true
		}
	}
	return CapabilityGrant{}, false
}

// AddCapabilityGrant records a grant, rejecting a duplicate target. The target
// is validated by the caller (see ValidateCapabilityTarget).
func (p *Project) AddCapabilityGrant(g CapabilityGrant) (CapabilityGrant, error) {
	if _, ok := p.CapabilityGrantByTarget(g.Target); ok {
		return CapabilityGrant{}, fmt.Errorf("capability %q already granted to island %q", g.Target, p.Name)
	}
	p.Capabilities = append(p.Capabilities, g)
	return g, nil
}

// RemoveCapabilityGrant removes the grant for target; ok=false if not present.
func (p *Project) RemoveCapabilityGrant(target string) (CapabilityGrant, bool) {
	for i, c := range p.Capabilities {
		if c.Target == target {
			removed := c
			p.Capabilities = append(p.Capabilities[:i], p.Capabilities[i+1:]...)
			return removed, true
		}
	}
	return CapabilityGrant{}, false
}

// ValidateCapabilityTarget checks a capability target name — a macOS Shortcut
// name or a ~/.dejima/capabilities/ script basename. It is intentionally more
// permissive than ValidateName (real Shortcut names carry spaces and mixed
// case) but stays safe as a single filename component: no path separators or
// traversal, no control characters, bounded length. The strict mode/ownership
// checks for the Linux script adapter happen at execution time, not here.
func ValidateCapabilityTarget(target string) error {
	if target == "" || len(target) > 128 {
		return fmt.Errorf("invalid capability target %q: must be 1–128 characters", target)
	}
	if target != strings.TrimSpace(target) {
		return fmt.Errorf("invalid capability target %q: no leading or trailing whitespace", target)
	}
	if strings.ContainsAny(target, `/\`) || strings.Contains(target, "..") {
		return fmt.Errorf(`invalid capability target %q: no path separators or ".."`, target)
	}
	for _, r := range target {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid capability target %q: contains a control character", target)
		}
	}
	return nil
}
