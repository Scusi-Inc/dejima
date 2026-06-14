package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfigBlock(t *testing.T) {
	got := sshConfigBlock("wildfire", "100.77.85.107", "2222")
	for _, want := range []string{
		"Host dejima-wildfire\n",
		"HostName 100.77.85.107\n",
		"Port 2222\n",
		"User wildfire\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
}

func TestInstallSSHConfigIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ssh", "config")

	block := sshConfigBlock("wildfire", "host", "2222")
	if err := installSSHConfig("wildfire", block); err != nil {
		t.Fatal(err)
	}
	// Re-install the same island: must NOT duplicate.
	if err := installSSHConfig("wildfire", block); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "Host dejima-wildfire"); n != 1 {
		t.Fatalf("expected exactly one wildfire entry, got %d:\n%s", n, data)
	}
	// 0600 — it can carry HostName/User but we still keep it tight.
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("config perms = %v, want 0600", info.Mode().Perm())
	}

	// A second, distinct island coexists.
	if err := installSSHConfig("foo", sshConfigBlock("foo", "host", "2222")); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "Host dejima-wildfire") || !strings.Contains(string(data), "Host dejima-foo") {
		t.Fatalf("both entries should be present:\n%s", data)
	}
	// dejima-foo's marker must not be a false-positive substring match against
	// a hypothetical dejima-foobar; re-installing foo stays idempotent.
	if err := installSSHConfig("foo", sshConfigBlock("foo", "host", "2222")); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if n := strings.Count(string(data), "Host dejima-foo\n"); n != 1 {
		t.Fatalf("expected one foo entry, got %d", n)
	}
}

func TestInstallSSHConfigPreservesExisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sshDir, "config")
	// Pre-existing content with no trailing blank line.
	if err := os.WriteFile(path, []byte("Host myserver\n    HostName example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installSSHConfig("wildfire", sshConfigBlock("wildfire", "host", "2222")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "Host myserver") {
		t.Errorf("clobbered pre-existing host:\n%s", s)
	}
	if !strings.Contains(s, "Host dejima-wildfire") {
		t.Errorf("new entry not appended:\n%s", s)
	}
	// The two Host blocks must be separated by a newline, not concatenated onto
	// the same line as the previous entry.
	if strings.Contains(s, "example.comHost") {
		t.Errorf("entries ran together without separator:\n%s", s)
	}
}
