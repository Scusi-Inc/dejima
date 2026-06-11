package project

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Port access modes. V1 grants are read-only; "rw" is reserved for the
// read-write milestone (docs/port-island-spec.md §6) and rejected at grant time
// until then.
const (
	PortModeRO = "ro"
	PortModeRW = "rw"
)

// PortScope is a single brokered host-filesystem grant for an island: a host
// directory the Port broker may Trade files from/to, and the policy on it.
//
// Access is deny-all by default — an island reaches host content only through
// an explicit scope. Scopes live in the island's host-side config (0600) and
// are never writable from inside the island, so an island cannot widen its own
// grant.
type PortScope struct {
	// Name is the short, slug handle used to address the scope in Trades and the
	// Ledger (e.g. "vault"). Derived from the host path's basename; unique within
	// the island.
	Name string `toml:"name"`
	// HostPath is the absolute host directory granted. The broker never serves
	// anything outside it.
	HostPath string `toml:"host_path"`
	// Mode is "ro" or "rw".
	Mode      string    `toml:"mode"`
	GrantedAt time.Time `toml:"granted_at"`
}

// PortScopeByName returns the scope with the given handle.
func (p *Project) PortScopeByName(name string) (*PortScope, bool) {
	for i := range p.Ports {
		if p.Ports[i].Name == name {
			return &p.Ports[i], true
		}
	}
	return nil, false
}

// PortScopeByHostPath returns the scope for the given (cleaned) host path.
func (p *Project) PortScopeByHostPath(hostPath string) (*PortScope, bool) {
	hp := filepath.Clean(hostPath)
	for i := range p.Ports {
		if p.Ports[i].HostPath == hp {
			return &p.Ports[i], true
		}
	}
	return nil, false
}

// AddPortScope cleans the host path, assigns a unique Name, and appends the
// scope. It errors if the host path is already granted. Returns the stored
// scope (with its assigned Name).
func (p *Project) AddPortScope(s PortScope) (PortScope, error) {
	s.HostPath = filepath.Clean(s.HostPath)
	if _, ok := p.PortScopeByHostPath(s.HostPath); ok {
		return PortScope{}, fmt.Errorf("a Port scope for %s is already granted", s.HostPath)
	}
	s.Name = p.uniqueScopeName(s.HostPath)
	p.Ports = append(p.Ports, s)
	return s, nil
}

// RemovePortScope drops the scope identified by key, which may be either its
// Name or its (cleaned) host path. Reports the removed scope and whether found.
func (p *Project) RemovePortScope(key string) (PortScope, bool) {
	ck := filepath.Clean(key)
	for i := range p.Ports {
		if p.Ports[i].Name == key || p.Ports[i].HostPath == ck {
			removed := p.Ports[i]
			p.Ports = append(p.Ports[:i], p.Ports[i+1:]...)
			return removed, true
		}
	}
	return PortScope{}, false
}

func (p *Project) uniqueScopeName(hostPath string) string {
	base := scopeBaseName(hostPath)
	name := base
	for i := 2; ; i++ {
		if _, ok := p.PortScopeByName(name); !ok {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func scopeBaseName(hostPath string) string {
	b := strings.ToLower(filepath.Base(filepath.Clean(hostPath)))
	var sb strings.Builder
	for _, r := range b {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	out := strings.Trim(sb.String(), "-_")
	if out == "" {
		out = "scope"
	}
	return out
}
