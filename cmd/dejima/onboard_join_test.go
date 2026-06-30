package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/clientcfg"
)

// withStdin swaps the shared stdin reader for a scripted one for the duration of
// a test (first-run prompts read from the package-level stdinReader).
func withStdin(t *testing.T, input string) {
	t.Helper()
	orig := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(input))
	t.Cleanup(func() { stdinReader = orig })
}

// TestJoinFromInviteGolden: the first-run join seam decodes a1's frozen invite
// vector and persists it as the active profile (host + token + role) — the same
// contract the CLI `dejima join` and the TUI switcher honor.
func TestJoinFromInviteGolden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p, name, err := joinFromInvite(goldenInvite)
	if err != nil {
		t.Fatalf("joinFromInvite(golden) errored: %v", err)
	}
	if p.Host != "minion.ts.net:7274" || p.Role != "operator" || name != "minion" {
		t.Fatalf("decoded payload/name = (%q, %q, name=%q), want (minion.ts.net:7274, operator, minion)", p.Host, p.Role, name)
	}
	cfg, err := clientcfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProfile != "minion" {
		t.Errorf("ActiveProfile = %q, want minion", cfg.ActiveProfile)
	}
	var saved *clientcfg.Profile
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == "minion" {
			saved = &cfg.Profiles[i]
		}
	}
	if saved == nil {
		t.Fatal("joined profile not persisted")
	}
	if saved.Host != "minion.ts.net:7274" || saved.Token != "sek_abc123" || saved.Role != "operator" {
		t.Errorf("saved profile = %+v, want host=minion.ts.net:7274 token=sek_abc123 role=operator", *saved)
	}
}

// TestJoinFromInviteBad: a malformed blob is a clean error and persists nothing.
func TestJoinFromInviteBad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, err := joinFromInvite("not-an-invite"); err == nil {
		t.Error("expected an error for a non-invite blob")
	}
	cfg, _ := clientcfg.Load()
	if len(cfg.Profiles) != 0 {
		t.Errorf("a failed decode must not save a profile, got %v", cfg.Profiles)
	}
}

// TestFirstRunJoinEmpty: pressing Enter at the invite prompt opens the dashboard
// (continue=true) without persisting anything — no half-join.
func TestFirstRunJoinEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withStdin(t, "\n")
	cont, err := firstRunJoin(context.Background())
	if err != nil || !cont {
		t.Fatalf("empty invite should continue to the dashboard, got (cont=%v, err=%v)", cont, err)
	}
	cfg, _ := clientcfg.Load()
	if len(cfg.Profiles) != 0 {
		t.Errorf("empty invite must not save a profile, got %v", cfg.Profiles)
	}
}

// TestFirstRunJoinBadBlob: a malformed paste surfaces the decoder's error and
// still opens the dashboard (never blocks), persisting nothing. No network: the
// decode fails before the connection verify.
func TestFirstRunJoinBadBlob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withStdin(t, "dejima-invite:!!!not-base64\n")
	cont, err := firstRunJoin(context.Background())
	if err != nil || !cont {
		t.Fatalf("a bad blob should continue to the dashboard, got (cont=%v, err=%v)", cont, err)
	}
	cfg, _ := clientcfg.Load()
	if len(cfg.Profiles) != 0 {
		t.Errorf("a bad blob must not save a profile, got %v", cfg.Profiles)
	}
}
