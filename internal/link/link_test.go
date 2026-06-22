package link

import "testing"

func TestGrantStoreDenyAllAndDirectional(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Deny-all: nothing is allowed before any grant.
	s, _ := Load()
	if _, ok := s.Allowed("a", "b", "t"); ok {
		t.Fatal("Allowed before any grant must be false (deny-all)")
	}

	s, err := Update(func(s *Store) error {
		s.Grant(Grant{From: "a", To: "b", Topic: "t"})
		return nil
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	// The granted direction/topic is allowed...
	if _, ok := s.Allowed("a", "b", "t"); !ok {
		t.Error("granted a→b/t should be allowed")
	}
	// ...but it's directional and topic-scoped: reverse and other topics are denied.
	if _, ok := s.Allowed("b", "a", "t"); ok {
		t.Error("reverse b→a must NOT be allowed by an a→b grant")
	}
	if _, ok := s.Allowed("a", "b", "other"); ok {
		t.Error("a different topic must NOT be allowed")
	}

	// Revoke severs it immediately and persists.
	if _, err := Update(func(s *Store) error {
		if !s.Revoke("a", "b", "t") {
			t.Error("Revoke should report true for an existing grant")
		}
		return nil
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	s, _ = Load()
	if _, ok := s.Allowed("a", "b", "t"); ok {
		t.Error("after revoke, a→b/t must be denied again")
	}
}
