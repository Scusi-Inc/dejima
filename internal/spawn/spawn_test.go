package spawn

import (
	"testing"
	"time"
)

func TestGrantSetGetRemovePersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// No grant → deny default.
	st, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("isl"); ok {
		t.Fatal("fresh store should have no grant (deny default)")
	}

	// Set a grant.
	if _, err := Update(func(s *Store) error {
		return s.Set(Grant{Island: "isl", MaxConcurrent: 3, MaxTotal: 10, Types: []string{"claude-code"}, TTL: time.Hour, PerAgentMemory: "512m"})
	}); err != nil {
		t.Fatal(err)
	}

	// Reload (new process) → grant persisted.
	st, _ = Load()
	g, ok := st.Get("isl")
	if !ok || g.MaxConcurrent != 3 || g.MaxTotal != 10 || g.TTL != time.Hour || g.PerAgentMemory != "512m" {
		t.Fatalf("grant not persisted correctly: %+v ok=%v", g, ok)
	}
	if g.CreatedAt.IsZero() {
		t.Error("Set should stamp CreatedAt")
	}

	// Revoke.
	if _, err := Update(func(s *Store) error {
		if !s.Remove("isl") {
			t.Error("Remove should report the grant existed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ = Load()
	if _, ok := st.Get("isl"); ok {
		t.Error("grant should be gone after revoke")
	}
}

func TestSetValidation(t *testing.T) {
	s := &Store{Grants: map[string]Grant{}}
	if err := s.Set(Grant{MaxConcurrent: 1}); err == nil {
		t.Error("empty island should error")
	}
	if err := s.Set(Grant{Island: "x", MaxConcurrent: 0}); err == nil {
		t.Error("max_concurrent <= 0 should error (use revoke instead)")
	}
	if err := s.Set(Grant{Island: "x", MaxConcurrent: 2}); err != nil {
		t.Fatalf("valid grant should set: %v", err)
	}
	if len(s.List()) != 1 {
		t.Errorf("List should return the one grant")
	}
}
