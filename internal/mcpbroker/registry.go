package mcpbroker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/aoos/dejima/internal/paths"
)

// ServerSpec is one operator-curated MCP server the broker may invoke. The set
// of specs — authored host-side in ~/.dejima/mcp/servers.toml, which the island
// cannot write — is the security boundary, exactly like the capability broker's
// ~/.dejima/capabilities/ scripts dir: the island can only invoke a server the
// user already chose to expose, and only one explicitly granted to it.
type ServerSpec struct {
	// Name is the handle a grant and a call address. Validated as a single safe
	// token (see ValidateServerName) — no separators, bounded length.
	Name string `toml:"name"`
	// Transport is the MCP transport. Only "stdio" in V1 (the dominant transport,
	// and the one with no network surface — the broker spawns the program and
	// speaks JSON-RPC over its pipes). Empty defaults to "stdio".
	Transport string `toml:"transport,omitempty"`
	// Command is the server program. The operator vouches for it by listing it in
	// the trust-checked registry; the broker execs it with a fixed argv and never
	// through a shell.
	Command string `toml:"command"`
	// Args is the fixed argv passed to Command. Never interpolated with request
	// data — request params travel as JSON-RPC on stdin.
	Args []string `toml:"args,omitempty"`
	// Env is an explicit "K=V" passthrough (e.g. an API key the server needs).
	// Only these — plus a minimal PATH and the island identity — reach the
	// process; the daemon's own environment is never inherited.
	Env []string `toml:"env,omitempty"`
}

// registryFile is the on-disk shape of ~/.dejima/mcp/servers.toml.
type registryFile struct {
	Servers []ServerSpec `toml:"servers"`
}

// Registry is the host-side catalogue of curated MCP servers. It is read fresh
// on every lookup (like the capability script adapter Lstats its target each
// call), so an operator editing servers.toml takes effect with no daemon
// restart. Path is the servers.toml location.
type Registry struct {
	Path string
}

// DefaultRegistry returns the registry at ~/.dejima/mcp/servers.toml.
func DefaultRegistry() (*Registry, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	return &Registry{Path: path}, nil
}

// registryPath resolves ~/.dejima/mcp/servers.toml, creating the mcp/ dir (0700)
// so the operator has a place to author it. The layout lives here rather than in
// internal/paths to keep the MCP-broker subsystem self-contained.
func registryPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "servers.toml"), nil
}

// List returns every curated server, or an empty slice when no registry exists
// yet (deny-all: nothing to invoke). A present-but-untrusted registry is an
// error — fail closed rather than trust a file some other account could edit.
func (r *Registry) List() ([]ServerSpec, error) {
	info, err := os.Lstat(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := trustRegistry(info); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerUntrusted, err)
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return nil, err
	}
	var rf registryFile
	if err := toml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse mcp registry %s: %w", r.Path, err)
	}
	return rf.Servers, nil
}

// Lookup returns the spec for name, or ErrServerNotFound. The name is matched
// exactly against the curated set; a server present in the registry but not yet
// granted to the island is still rejected upstream (deny-all grant check).
func (r *Registry) Lookup(name string) (ServerSpec, error) {
	servers, err := r.List()
	if err != nil {
		return ServerSpec{}, err
	}
	for _, s := range servers {
		if s.Name == name {
			if s.Transport != "" && s.Transport != "stdio" {
				return ServerSpec{}, fmt.Errorf("%w: server %q uses unsupported transport %q", ErrProtocol, name, s.Transport)
			}
			if strings.TrimSpace(s.Command) == "" {
				return ServerSpec{}, fmt.Errorf("%w: server %q has no command", ErrProtocol, name)
			}
			return s, nil
		}
	}
	return ServerSpec{}, ErrServerNotFound
}

// trustRegistry enforces the curation boundary on servers.toml: only a regular
// file owned by the daemon user and not group/world-writable is trusted. This
// stops a compromised island (which cannot write the file) or another local
// account from minting server entries the broker would then exec.
func trustRegistry(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (symlinks and directories are rejected)")
	}
	if mode := info.Mode().Perm(); mode&0o022 != 0 {
		return fmt.Errorf("group/world-writable (%#o) — chmod go-w", mode)
	}
	return ownedByDaemon(info)
}

// ValidateServerName checks a registry server name — the handle a grant and a
// call address. It must be a single safe token: 1–64 chars, no path separators
// or traversal, no whitespace or control characters. (Server commands can be
// anything the operator curates; the *name* stays a clean identifier.)
func ValidateServerName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("invalid mcp server name %q: must be 1–64 characters", name)
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("invalid mcp server name %q: no leading or trailing whitespace", name)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf(`invalid mcp server name %q: no path separators or ".."`, name)
	}
	for _, ch := range name {
		if ch < 0x20 || ch == 0x7f || ch == ' ' {
			return fmt.Errorf("invalid mcp server name %q: no spaces or control characters", name)
		}
	}
	return nil
}
