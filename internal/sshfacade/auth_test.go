package sshfacade

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/aoos/dejima/internal/project"
)

// newKey returns a throwaway ed25519 SSH public key and its authorized_keys line.
func newKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub, string(ssh.MarshalAuthorizedKey(sshPub))
}

func TestHostSignerPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s1, err := HostSigner()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := HostSigner() // second call must reuse the persisted key
	if err != nil {
		t.Fatal(err)
	}
	if string(s1.PublicKey().Marshal()) != string(s2.PublicKey().Marshal()) {
		t.Fatal("host key not stable across calls")
	}
}

func TestAuthorizeRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (&project.Project{Name: "isle"}).Save(); err != nil {
		t.Fatal(err)
	}
	good, goodLine := newKey(t)
	other, _ := newKey(t)

	// No authorized_keys yet → deny.
	if ok, err := Authorize("isle", good); err != nil || ok {
		t.Fatalf("pre-add Authorize = (%v,%v), want (false,nil)", ok, err)
	}

	if _, err := AddAuthorizedKey("isle", goodLine); err != nil {
		t.Fatalf("AddAuthorizedKey: %v", err)
	}
	// Idempotent re-add.
	if _, err := AddAuthorizedKey("isle", goodLine); err != nil {
		t.Fatalf("re-add: %v", err)
	}

	if ok, err := Authorize("isle", good); err != nil || !ok {
		t.Fatalf("authorized key Authorize = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := Authorize("isle", other); err != nil || ok {
		t.Fatalf("unknown key Authorize = (%v,%v), want (false,nil)", ok, err)
	}
}

func TestAddRejectsGarbage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := AddAuthorizedKey("isle", "not a key"); err == nil {
		t.Fatal("expected error for non-key line")
	}
}

func TestAuthorizeRejectsBadIslandName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	good, _ := newKey(t)
	if _, err := Authorize("../etc", good); err == nil {
		t.Fatal("expected error for traversal island name")
	}
}
