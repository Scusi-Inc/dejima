package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `dejima ssh enroll --key <missing>` must fail on the key file, before it ever
// contacts the daemon — enrolling is the remedy `dejima agent open` points at
// when the façade refuses a device, so a typo'd path has to say so plainly
// rather than surface as a connection error.
func TestSSHEnrollRejectsMissingKeyFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope.pub")
	_, err := runCLI(t, "ssh", "enroll", "--key", missing)
	if err == nil {
		t.Fatal("enroll should fail when the named key file doesn't exist")
	}
	if !strings.Contains(err.Error(), "nope.pub") {
		t.Errorf("error should name the missing file, got: %v", err)
	}
}

// enroll generates a keypair when the machine has none, which is what makes it
// a one-command remedy on a fresh device.
func TestEnsureLocalKeyGeneratesWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pub, err := ensureLocalKey()
	if err != nil {
		t.Fatalf("ensureLocalKey on a machine with no key: %v", err)
	}
	if !strings.HasSuffix(pub, ".pub") {
		t.Errorf("should return the PUBLIC key path, got %q", pub)
	}
	b, err := os.ReadFile(pub)
	if err != nil {
		t.Fatalf("generated key is not readable: %v", err)
	}
	if !strings.HasPrefix(string(b), "ssh-") {
		t.Errorf("generated key doesn't look like an OpenSSH public key: %q", b)
	}
	// The private half must not be world-readable — this key authorizes every
	// island on the daemon.
	priv := strings.TrimSuffix(pub, ".pub")
	fi, err := os.Stat(priv)
	if err != nil {
		t.Fatalf("no private key alongside %s: %v", pub, err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("private key mode = %04o, want no group/other access", perm)
	}
}

// A second call must reuse the existing key rather than rolling a new one —
// re-running enroll is the documented thing to do after enabling the façade,
// and rotating the key there would silently deauthorize the device.
func TestEnsureLocalKeyIsStable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := ensureLocalKey()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureLocalKey()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("path changed on the second call: %q then %q", first, second)
	}
	after, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the key was regenerated; re-running enroll would deauthorize this device")
	}
}
