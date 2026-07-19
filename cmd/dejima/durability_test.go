package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClientErrorsOnUnreadableConfig: a corrupt saved config (beyond .bak
// recovery), with no env/flag target, must surface as a clear actionable error
// from client() — NOT a silent fall-through to the local socket (the
// silent-lockout bug: an update wiped the connection and every command quietly
// tried a dead local socket).
func TestClientErrorsOnUnreadableConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_HOST", "") // no env target; no -p/--host flag in a unit test

	dir := filepath.Join(os.Getenv("HOME"), ".dejima")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Corrupt both client.json AND its backup, so Load can't recover → it errors.
	os.WriteFile(filepath.Join(dir, "client.json"), []byte("{corrupt"), 0o600)
	os.WriteFile(filepath.Join(dir, "client.json.bak"), []byte("also bad"), 0o600)

	if _, _, source := resolveTarget(); source != "unreadable" {
		t.Fatalf("resolveTarget source = %q, want unreadable (must not silently fall to local)", source)
	}
	_, err := client()
	if err == nil {
		t.Fatal("client() must error on an unreadable config, not build a dead local-socket client")
	}
	if !strings.Contains(err.Error(), "unreadable") || !strings.Contains(err.Error(), "dejima join") {
		t.Errorf("client() error should name the problem + the recovery, got: %v", err)
	}
}
