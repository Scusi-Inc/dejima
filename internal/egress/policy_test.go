package egress

import (
	"path/filepath"
	"testing"
)

func TestPolicyAllowModes(t *testing.T) {
	s := OpenPolicy(filepath.Join(t.TempDir(), "p.json"))

	// No policy = observe = allow everything (Phase 1 behavior preserved).
	if !s.Allow("isl", "anything.example") {
		t.Fatal("default (no policy) must allow all")
	}

	// Deny-list blocks even in observe mode, and covers subdomains.
	if _, err := s.Apply("isl", PolicyPatch{AddDeny: []string{"evil.com"}}); err != nil {
		t.Fatal(err)
	}
	if s.Allow("isl", "evil.com") || s.Allow("isl", "api.evil.com") {
		t.Error("deny-list (incl. subdomains) must block in observe mode")
	}
	if !s.Allow("isl", "good.com") {
		t.Error("observe mode still allows non-denied hosts")
	}

	// Enforce mode = deny-all except the allow-list (subdomain match).
	if _, err := s.Apply("isl", PolicyPatch{Mode: ModeEnforce, AddAllow: []string{"github.com"}}); err != nil {
		t.Fatal(err)
	}
	if !s.Allow("isl", "github.com") || !s.Allow("isl", "api.github.com") {
		t.Error("enforce must allow the allow-listed domain + its subdomains")
	}
	if s.Allow("isl", "pypi.org") {
		t.Error("enforce must deny anything not allow-listed")
	}
	// Deny still wins over allow.
	if _, err := s.Apply("isl", PolicyPatch{AddAllow: []string{"evil.com"}}); err != nil {
		t.Fatal(err)
	}
	if s.Allow("isl", "evil.com") {
		t.Error("deny must win even when also allow-listed")
	}

	// Policy is per-island: a different island is unaffected.
	if !s.Allow("other", "pypi.org") {
		t.Error("policy must not leak across islands")
	}

	// Unknown mode is rejected.
	if _, err := s.Apply("isl", PolicyPatch{Mode: "wat"}); err == nil {
		t.Error("unknown mode should error")
	}
}

func TestPolicyPersistsAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	s1 := OpenPolicy(path)
	if _, err := s1.Apply("isl", PolicyPatch{Mode: ModeEnforce, AddAllow: []string{"github.com", "pypi.org"}}); err != nil {
		t.Fatal(err)
	}
	// Reopen (simulates daemon restart) → policy survived.
	s2 := OpenPolicy(path)
	got := s2.Get("isl")
	if got.Mode != ModeEnforce || len(got.Allow) != 2 {
		t.Fatalf("policy not persisted: %+v", got)
	}
	// Remove one host.
	if _, err := s2.Apply("isl", PolicyPatch{RemoveAllow: []string{"pypi.org"}}); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("isl"); len(got.Allow) != 1 || got.Allow[0] != "github.com" {
		t.Errorf("remove failed: %+v", got)
	}
}
