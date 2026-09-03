package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedKnownHostsArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home

	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAExampleKeyData comment"
	args, err := managedKnownHostsArgs("100.101.102.103", "2222", key)
	if err != nil {
		t.Fatal(err)
	}

	// It must point ssh at a dejima-owned file with strict checking on.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "UserKnownHostsFile=") || !strings.Contains(joined, "StrictHostKeyChecking=yes") {
		t.Fatalf("expected UserKnownHostsFile + strict checking, got %v", args)
	}

	// The file holds the current key under the "[host]:port" form (non-default port).
	path := filepath.Join(home, ".dejima", "known_hosts")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("known_hosts not written: %v", err)
	}
	got := strings.TrimSpace(string(b))
	if got != "[100.101.102.103]:2222 "+key {
		t.Errorf("known_hosts line = %q, want the bracketed host:port + key", got)
	}

	// A rotated key must REPLACE the file, not append — the whole point of
	// self-healing. Rewrite with a new key and confirm the old one is gone.
	newKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAARotatedKeyData comment"
	if _, err := managedKnownHostsArgs("100.101.102.103", "2222", newKey); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "ExampleKeyData") {
		t.Error("stale key still present after rotation — file should be overwritten")
	}
	if !strings.Contains(string(b), "RotatedKeyData") {
		t.Error("new key not written after rotation")
	}

	// No key from the daemon (older daemon) → no opts, ssh falls back to default.
	args, err = managedKnownHostsArgs("h", "2222", "")
	if err != nil || args != nil {
		t.Errorf("empty host key should yield nil opts, got %v (err %v)", args, err)
	}
}
