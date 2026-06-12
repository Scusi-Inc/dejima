package sshfacade

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
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

func TestListAndRevoke(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (&project.Project{Name: "isle"}).Save(); err != nil {
		t.Fatal(err)
	}
	k1, line1 := newKey(t)
	k2, line2 := newKey(t)
	// authorized_keys lines carry a comment; AddAuthorizedKey re-marshals, so add
	// with a comment to confirm list/revoke preserve it.
	if _, err := AddAuthorizedKey("isle", strings.TrimRight(line1, "\n")+" alice@laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAuthorizedKey("isle", strings.TrimRight(line2, "\n")+" bob@desktop"); err != nil {
		t.Fatal(err)
	}

	keys, err := ListAuthorizedKeys("isle")
	if err != nil || len(keys) != 2 {
		t.Fatalf("list = %d keys (%v), want 2", len(keys), err)
	}

	fp1 := ssh.FingerprintSHA256(k1)
	// Revoke a non-existent fingerprint: must change nothing.
	if n, _ := RemoveAuthorizedKey("isle", "SHA256:does-not-exist"); n != 0 {
		t.Fatalf("revoke miss removed %d, want 0", n)
	}
	if keys, _ := ListAuthorizedKeys("isle"); len(keys) != 2 {
		t.Fatalf("no-match revoke truncated the file: %d keys left", len(keys))
	}

	// Revoke k1 by fingerprint; k2 (and its comment) survive.
	if n, err := RemoveAuthorizedKey("isle", fp1); err != nil || n != 1 {
		t.Fatalf("revoke k1 = (%d,%v), want (1,nil)", n, err)
	}
	keys, _ = ListAuthorizedKeys("isle")
	if len(keys) != 1 || keys[0].Fingerprint != ssh.FingerprintSHA256(k2) {
		t.Fatalf("after revoke: keys=%+v, want only k2", keys)
	}
	if keys[0].Comment != "bob@desktop" {
		t.Errorf("comment not preserved on rewrite: %q", keys[0].Comment)
	}
	// k1 no longer authorizes; k2 still does.
	if ok, _ := Authorize("isle", k1); ok {
		t.Error("revoked key still authorizes")
	}
	if ok, _ := Authorize("isle", k2); !ok {
		t.Error("surviving key no longer authorizes")
	}

	// Revoke all.
	if n, err := RemoveAllAuthorizedKeys("isle"); err != nil || n != 1 {
		t.Fatalf("revoke all = (%d,%v), want (1,nil)", n, err)
	}
	if keys, _ := ListAuthorizedKeys("isle"); len(keys) != 0 {
		t.Fatalf("after revoke --all: %d keys left", len(keys))
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
