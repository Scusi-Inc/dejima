package paths

import "testing"

// When not running as root, userHome must ignore SUDO_USER and use the normal
// home ($HOME) — the sudo-recovery path is gated on euid 0. (The root branch
// itself needs root to exercise and is covered operationally.)
func TestUserHomeIgnoresSudoUserWhenNotRoot(t *testing.T) {
	t.Setenv("SUDO_USER", "someone-else")
	t.Setenv("HOME", "/tmp/dejima-fake-home")
	got, err := userHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/dejima-fake-home" {
		t.Errorf("userHome() = %q, want /tmp/dejima-fake-home (SUDO_USER must be ignored when not root)", got)
	}
}
