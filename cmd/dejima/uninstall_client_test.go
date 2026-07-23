package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The whole point of --client: it must NEVER contact a daemon. A laptop or
// Windows box that only drives a remote server has no local daemon, so the full
// uninstall's "is the daemon running?" is exactly what this avoids. It only
// removes this machine's connection config and points at removing the binary.
func TestUninstallClientTouchesNoDaemonAndRemovesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".dejima")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(dir, "client.json")
	host := filepath.Join(dir, "host.json")
	for _, p := range []string{client, host} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// yes=true so it runs without a prompt. If this reached a daemon it would
	// hang or error on a socket that doesn't exist — the test's temp HOME has no
	// daemon and no socket.
	if err := uninstallClient(true); err != nil {
		t.Fatalf("uninstallClient errored (did it try to reach a daemon?): %v", err)
	}

	for _, p := range []string{client, host} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived --client uninstall", filepath.Base(p))
		}
	}
}

// Running on a machine that was never configured must not error — a bare CLI
// install with no saved connection is a normal case.
func TestUninstallClientOnUnconfiguredMachine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := uninstallClient(true); err != nil {
		t.Errorf("--client on an unconfigured machine should be a clean no-op, got: %v", err)
	}
}
