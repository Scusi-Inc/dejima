package mcpbroker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeRegistry(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

const sampleRegistry = `[[servers]]
name = "files"
command = "/usr/bin/mcp-files"
args = ["--root", "/data"]

[[servers]]
name = "fetch"
transport = "stdio"
command = "/usr/bin/mcp-fetch"
env = ["TOKEN=abc"]
`

func TestRegistry_LookupAndList(t *testing.T) {
	r := &Registry{Path: writeRegistry(t, sampleRegistry, 0o600)}
	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 servers, got %d", len(list))
	}
	s, err := r.Lookup("fetch")
	if err != nil {
		t.Fatalf("Lookup fetch: %v", err)
	}
	if s.Command != "/usr/bin/mcp-fetch" || len(s.Env) != 1 {
		t.Fatalf("unexpected spec: %+v", s)
	}
}

func TestRegistry_NotFound(t *testing.T) {
	r := &Registry{Path: writeRegistry(t, sampleRegistry, 0o600)}
	if _, err := r.Lookup("ghost"); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("want ErrServerNotFound, got %v", err)
	}
}

// A missing registry is deny-all, not an error: nothing to invoke.
func TestRegistry_AbsentIsEmpty(t *testing.T) {
	r := &Registry{Path: filepath.Join(t.TempDir(), "nope.toml")}
	list, err := r.List()
	if err != nil {
		t.Fatalf("absent registry should not error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty, got %d", len(list))
	}
	if _, err := r.Lookup("files"); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("want ErrServerNotFound from absent registry, got %v", err)
	}
}

// A world-writable registry can't be trusted to name safe server programs.
func TestRegistry_WorldWritableUntrusted(t *testing.T) {
	r := &Registry{Path: writeRegistry(t, sampleRegistry, 0o666)}
	if _, err := r.List(); !errors.Is(err, ErrServerUntrusted) {
		t.Fatalf("want ErrServerUntrusted for 0666 registry, got %v", err)
	}
}

func TestRegistry_UnsupportedTransport(t *testing.T) {
	reg := `[[servers]]
name = "remote"
transport = "http"
command = "x"
`
	r := &Registry{Path: writeRegistry(t, reg, 0o600)}
	if _, err := r.Lookup("remote"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol for unsupported transport, got %v", err)
	}
}

func TestRegistry_EmptyCommand(t *testing.T) {
	reg := `[[servers]]
name = "broken"
command = "  "
`
	r := &Registry{Path: writeRegistry(t, reg, 0o600)}
	if _, err := r.Lookup("broken"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol for empty command, got %v", err)
	}
}

func TestValidateServerName(t *testing.T) {
	good := []string{"files", "mcp-fetch", "a", "Server_1", "x.y"}
	for _, n := range good {
		if err := ValidateServerName(n); err != nil {
			t.Errorf("ValidateServerName(%q) unexpected error: %v", n, err)
		}
	}
	bad := []string{"", "a/b", "../x", "a b", "has\ttab", "\x00null"}
	for _, n := range bad {
		if err := ValidateServerName(n); err == nil {
			t.Errorf("ValidateServerName(%q) expected error", n)
		}
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateServerName(string(long)); err == nil {
		t.Error("ValidateServerName(65 chars) expected error")
	}
}
