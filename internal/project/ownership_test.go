package project

import (
	"os"
	"strings"
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

// The default host owner must describe THIS HOST — not a literal handle.
//
// It used to return "aoos". Ownership is stamped server-authoritatively (never
// from the create request, which a teammate could forge), so this function is
// the only thing that names the operator, and it named one specific person on
// every install. Someone else's machine reported every island they created as
// `owner: aoos` — while Owner is documented as a "free-form creator label (e.g.
// alice@laptop)" for attributing islands per person.
//
// Asserted as a PROPERTY, not against this runner's username: pinning the exact
// string would just re-pin a constant, which is the bug.
func TestHostOwnerDescribesThisHost(t *testing.T) {
	got := HostOwner()
	if got == "" {
		t.Fatal("host owner is empty; islands would be stamped with nothing")
	}
	if strings.EqualFold(got, LegacyHostOwner) {
		t.Errorf("host owner is still the hardcoded %q — every operator's islands "+
			"are attributed to one person who is probably not them", LegacyHostOwner)
	}
	// user@hostname is the documented shape and what the CLI's defaultOwner uses.
	if host, err := os.Hostname(); err == nil && host != "" {
		if !strings.HasSuffix(got, "@"+host) {
			t.Errorf("host owner %q does not identify this host (%q)", got, host)
		}
	}
}

func TestHostOwnerConfigurable(t *testing.T) {
	t.Setenv("DEJIMA_HOST_OWNER", "acme")
	if HostOwner() != "acme" {
		t.Errorf("DEJIMA_HOST_OWNER override = %q, want acme", HostOwner())
	}
}

// Islands stamped before the host label derived from the host carry the old
// literal. A gate that compared == HostOwner() would stop matching every one of
// them — a fix that only reaches things created after it, which is the shape
// this repo keeps re-learning. IsHostOwner is what callers must use.
func TestIsHostOwnerAcceptsTheLegacyStamp(t *testing.T) {
	if !IsHostOwner(LegacyHostOwner) {
		t.Errorf("an island stamped %q is no longer recognised as the host's own; "+
			"owner-gated behavior silently changed for every existing island", LegacyHostOwner)
	}
	if !IsHostOwner(HostOwner()) {
		t.Error("the current host owner is not recognised as the host's own")
	}
	if IsHostOwner("someone-else@their-laptop") {
		t.Error("a teammate's island was claimed as host-owned")
	}
	if IsHostOwner("") || IsHostOwner("   ") {
		t.Error("an unstamped island must not be claimed as host-owned")
	}
}
