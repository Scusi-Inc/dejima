package project

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/aoos/dejima/internal/paths"
)

// MCP-broker grants live in a *sidecar* file (~/.dejima/projects/<island>/
// mcp.toml), deliberately NOT as a field on the shared Project struct. The Port
// and capability grants are Project fields, but the MCP lane and the team-auth
// lane both extend Project concurrently — keeping these grants in their own file
// means the two never touch the same struct and never conflict. The on-disk
// shape and the deny-all semantics are otherwise identical to capabilities.go:
// an island may invoke only the MCP servers granted here; empty ⇒ deny-all.
//
// See docs/mcp-broker-spec.md.

// MCPGrant is an island's permission to invoke one named, host-curated MCP
// server (an entry in ~/.dejima/mcp/servers.toml — see internal/mcpbroker). The
// transport and command behind the name are chosen host-side, not stored per
// grant; the grant is only the island↦server-name permission.
type MCPGrant struct {
	Server    string    `toml:"server"`
	GrantedAt time.Time `toml:"granted_at"`
}

// mcpGrantsFile is the on-disk shape of the per-island mcp.toml sidecar.
type mcpGrantsFile struct {
	Grants []MCPGrant `toml:"grants"`
}

// mcpGrantsPath returns ~/.dejima/projects/<island>/mcp.toml. It reuses
// ProjectDir (which creates the dir), so a grant for an existing island always
// has a place to land. Callers confirm the island exists (project.Load) first.
func mcpGrantsPath(island string) (string, error) {
	dir, err := paths.ProjectDir(island)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp.toml"), nil
}

// MCPGrantsFor returns an island's MCP-server grants (empty ⇒ deny-all). A
// missing sidecar is the deny-all default, not an error.
func MCPGrantsFor(island string) ([]MCPGrant, error) {
	path, err := mcpGrantsPath(island)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f mcpGrantsFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse mcp grants for %q: %w", island, err)
	}
	return f.Grants, nil
}

// saveMCPGrants writes the grant set back to the sidecar (0600). An empty set
// leaves an empty file — a present, deny-all record — rather than deleting it.
func saveMCPGrants(island string, grants []MCPGrant) error {
	path, err := mcpGrantsPath(island)
	if err != nil {
		return err
	}
	data, err := toml.Marshal(mcpGrantsFile{Grants: grants})
	if err != nil {
		return fmt.Errorf("marshal mcp grants for %q: %w", island, err)
	}
	return os.WriteFile(path, data, 0o600)
}

// MCPGrantByServer returns the grant for server, or ok=false.
func MCPGrantByServer(island, server string) (MCPGrant, bool, error) {
	grants, err := MCPGrantsFor(island)
	if err != nil {
		return MCPGrant{}, false, err
	}
	for _, g := range grants {
		if g.Server == server {
			return g, true, nil
		}
	}
	return MCPGrant{}, false, nil
}

// AddMCPGrant records a grant, rejecting a duplicate server name. The server
// name is validated by the caller (mcpbroker.ValidateServerName).
func AddMCPGrant(island string, g MCPGrant) (MCPGrant, error) {
	grants, err := MCPGrantsFor(island)
	if err != nil {
		return MCPGrant{}, err
	}
	for _, existing := range grants {
		if existing.Server == g.Server {
			return MCPGrant{}, fmt.Errorf("mcp server %q already granted to island %q", g.Server, island)
		}
	}
	grants = append(grants, g)
	if err := saveMCPGrants(island, grants); err != nil {
		return MCPGrant{}, err
	}
	return g, nil
}

// RemoveMCPGrant drops the grant for server; ok=false if not present.
func RemoveMCPGrant(island, server string) (MCPGrant, bool, error) {
	grants, err := MCPGrantsFor(island)
	if err != nil {
		return MCPGrant{}, false, err
	}
	for i, g := range grants {
		if g.Server == server {
			grants = append(grants[:i], grants[i+1:]...)
			if err := saveMCPGrants(island, grants); err != nil {
				return MCPGrant{}, false, err
			}
			return g, true, nil
		}
	}
	return MCPGrant{}, false, nil
}
