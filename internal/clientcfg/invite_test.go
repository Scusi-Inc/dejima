package clientcfg

import (
	"testing"

	"github.com/aoos/dejima/internal/invite"
)

func TestSaveInviteAndTokenForHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p := invite.Payload{V: 1, Host: "minion.ts.net:7274", Token: "sek_xyz", Role: "operator", Islands: []string{"webapp"}, Name: "minion"}
	name, err := SaveInvite(p)
	if err != nil {
		t.Fatal(err)
	}
	if name != "minion" {
		t.Errorf("profile name = %q, want minion", name)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProfile != "minion" {
		t.Errorf("active profile = %q, want minion", cfg.ActiveProfile)
	}
	if got := cfg.TokenForHost("minion.ts.net:7274"); got != "sek_xyz" {
		t.Errorf("TokenForHost = %q, want sek_xyz", got)
	}
	if got := cfg.TokenForHost("other.host:7274"); got != "" {
		t.Errorf("TokenForHost(unknown) = %q, want empty", got)
	}

	// Re-join rotates the token in place (no duplicate profile).
	p.Token = "sek_rotated"
	if _, err := SaveInvite(p); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	if len(cfg.Profiles) != 1 {
		t.Errorf("re-join created a duplicate: %d profiles, want 1", len(cfg.Profiles))
	}
	if got := cfg.TokenForHost("minion.ts.net:7274"); got != "sek_rotated" {
		t.Errorf("after re-join TokenForHost = %q, want sek_rotated", got)
	}
}

// TestSaveInviteDerivesName: no Name in the invite → derive from the host's
// first DNS label.
func TestSaveInviteDerivesName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	name, err := SaveInvite(invite.Payload{V: 1, Host: "box.example.com:7274", Token: "t", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "box" {
		t.Errorf("derived name = %q, want box", name)
	}
}
