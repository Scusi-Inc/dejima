package project

import (
	"testing"
	"time"
)

// TestOwnershipMigrationOnLoad: a project persisted without an owner (predating
// multi-tenant ownership) is stamped to the host owner on the next Load, and
// re-saved once — idempotently.
func TestOwnershipMigrationOnLoad(t *testing.T) {
	setHome(t)

	// Persist a project with NO owner (the pre-ownership shape).
	p := &Project{Name: "legacy", DesiredState: StateRunning, CreatedAt: time.Now().UTC()}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if p.Owner != "" {
		t.Fatalf("precondition: owner should be empty, got %q", p.Owner)
	}

	got, err := Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != HostOwner() {
		t.Errorf("migrated owner = %q, want host owner %q", got.Owner, HostOwner())
	}

	// Persisted: a fresh Load reads the migrated owner (no re-migration needed).
	again, err := Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if again.Owner != HostOwner() {
		t.Errorf("owner not persisted: %q", again.Owner)
	}
}

func TestHostOwnerConfigurable(t *testing.T) {
	if HostOwner() != "aoos" {
		t.Errorf("default host owner = %q, want aoos", HostOwner())
	}
	t.Setenv("DEJIMA_HOST_OWNER", "acme")
	if HostOwner() != "acme" {
		t.Errorf("DEJIMA_HOST_OWNER override = %q, want acme", HostOwner())
	}
}
