package hosttmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempRoot points paths.Root at a temp dir via HOME so ConfigPath writes
// somewhere isolated. paths.userHome resolves $HOME on non-root.
func withTempRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "")
	return home
}

func TestConfigPathMaterializesEmbeddedConfig(t *testing.T) {
	home := withTempRoot(t)
	p, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if want := filepath.Join(home, ".dejima", configName); p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read materialized config: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"allow-passthrough on",
		"extended-keys on",
		`source-file -q "~/.tmux.conf"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("materialized config missing %q", want)
		}
	}
}

func TestConfigPathIdempotentAndRefreshes(t *testing.T) {
	withTempRoot(t)
	p, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	// A second call returns the same path and does not error.
	p2, err := ConfigPath()
	if err != nil || p2 != p {
		t.Fatalf("second ConfigPath = (%q, %v), want (%q, nil)", p2, err, p)
	}
	// A drifted file is rewritten back to the embedded content.
	if err := os.WriteFile(p, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigPath(); err != nil {
		t.Fatalf("ConfigPath after drift: %v", err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(config) {
		t.Fatalf("config not refreshed after drift")
	}
}

func TestNewSessionArgs(t *testing.T) {
	withTempRoot(t)
	argv := NewSessionArgs("new-session", "-d", "-s", "x")
	if len(argv) != 7 || argv[0] != "tmux" || argv[1] != "-f" {
		t.Fatalf("argv = %v, want tmux -f <conf> new-session -d -s x", argv)
	}
	if argv[3] != "new-session" || argv[6] != "x" {
		t.Fatalf("tail argv wrong: %v", argv)
	}
}
