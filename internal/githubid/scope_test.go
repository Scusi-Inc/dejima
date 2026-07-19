package githubid

import "testing"

// TestResolveForIslandContainment is the security-critical test: an operator's
// identity resolves ONLY into their own islands (never another tenant's), a host
// island uses host identities, and a host-Shared identity reaches any tenant.
func TestResolveForIslandContainment(t *testing.T) {
	s := &Store{}
	s.PutOwned(Identity{Name: "work", Login: "host", Token: "H", Owner: ""})                // host identity
	s.PutOwned(Identity{Name: "work", Login: "amanda", Token: "A", Owner: "amanda"})        // amanda's own "work"
	s.PutOwned(Identity{Name: "personal", Login: "bob", Token: "B", Owner: "bob"})          // bob's
	s.PutOwned(Identity{Name: "org", Login: "orgbot", Token: "O", Owner: "", Shared: true}) // host-shared

	cases := []struct {
		island, name string
		wantToken    string // "" = expect NOT resolved
	}{
		{"", "work", "H"},             // host island → host identity
		{"amanda", "work", "A"},       // amanda's island → amanda's own "work" (NOT the host "work")
		{"bob", "work", ""},           // bob has no "work"; host "work" is NOT shared → denied
		{"amanda", "personal", ""},    // amanda cannot reach bob's "personal"
		{"amanda", "org", "O"},        // host-shared reaches any tenant
		{"bob", "org", "O"},           // host-shared reaches any tenant
		{"", "org", "O"},              // host island uses host-shared too
		{"amanda", "nonexistent", ""}, // nothing
	}
	for _, c := range cases {
		id, ok := s.ResolveForIsland(c.island, c.name)
		if c.wantToken == "" {
			if ok {
				t.Errorf("ResolveForIsland(%q,%q) resolved %q — must be denied", c.island, c.name, id.Token)
			}
			continue
		}
		if !ok || id.Token != c.wantToken {
			t.Errorf("ResolveForIsland(%q,%q) = (%q,%v), want token %q", c.island, c.name, id.Token, ok, c.wantToken)
		}
	}
}

// TestOwnerDefaultsAndList: per-tenant defaults resolve independently, and list
// visibility is tenant-scoped (+ host-shared), with the host owner seeing all.
func TestOwnerDefaultsAndList(t *testing.T) {
	s := &Store{}
	s.PutOwned(Identity{Name: "a1", Login: "amanda", Owner: "amanda"}) // becomes amanda's default (first)
	s.PutOwned(Identity{Name: "b1", Login: "bob", Owner: "bob"})
	s.PutOwned(Identity{Name: "shared", Login: "org", Owner: "", Shared: true})

	// amanda's empty-name resolution uses HER default, not bob's.
	if id, ok := s.ResolveForIsland("amanda", ""); !ok || id.Login != "amanda" {
		t.Errorf("amanda default = %+v ok=%v", id, ok)
	}

	// amanda sees her own + the host-shared, not bob's.
	names := map[string]bool{}
	for _, m := range s.ListForOwner("amanda", false) {
		names[m.Name] = true
	}
	if !names["a1"] || !names["shared"] || names["b1"] {
		t.Errorf("amanda list = %v, want a1+shared, not b1", names)
	}

	// The host owner (ownsAll) sees everything.
	if got := len(s.ListForOwner("", true)); got != 3 {
		t.Errorf("host owner sees %d identities, want 3", got)
	}
}

// TestDeleteOwnedIsTenantScoped: deleting is per-tenant — amanda deleting "work"
// never touches bob's "work".
func TestDeleteOwnedIsTenantScoped(t *testing.T) {
	s := &Store{}
	s.PutOwned(Identity{Name: "work", Owner: "amanda"})
	s.PutOwned(Identity{Name: "work", Owner: "bob"})
	if !s.DeleteOwned("amanda", "work") {
		t.Fatal("delete amanda/work should succeed")
	}
	if _, ok := s.Find("amanda", "work"); ok {
		t.Error("amanda/work should be gone")
	}
	if _, ok := s.Find("bob", "work"); !ok {
		t.Error("bob/work must be untouched by amanda's delete")
	}
}

// TestLegacyMigration: an old bare-name Identities map loads as host ("") idents.
func TestLegacyMigration(t *testing.T) {
	s := &Store{
		Default:    "work",
		Identities: map[string]Identity{"work": {Name: "work", Login: "legacy", Token: "L"}},
	}
	s.migrateLegacy()
	if s.Identities != nil {
		t.Error("legacy map should be cleared after migration")
	}
	id, ok := s.ResolveForIsland("", "work")
	if !ok || id.Login != "legacy" || id.Owner != "" {
		t.Errorf("migrated identity = %+v ok=%v, want host-owned legacy", id, ok)
	}
}
