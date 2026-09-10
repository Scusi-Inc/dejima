package project

import (
	"testing"
	"time"
)

// An island stamped with the LEGACY host-owner literal must be re-stamped onto
// the current host owner on load.
//
// HostOwner used to return the constant "aoos" for every install and now
// derives from the host. Nothing reconciled the two: every owner comparison in
// the codebase is a raw ==, so after a daemon upgrade the caller is
// "user@host" while the islands still say "aoos", and they stop being the same
// tenant.
//
// The dashboard's ownership lens defaults to your-islands-only and filters on
// exactly that equality. An operator's whole fleet vanished from the TUI while
// `dejima ls` — which has no lens — still listed it and the footer still counted
// it. They reported every island and agent as missing. Nothing was.
func TestLegacyOwnerIsRestampedOnLoad(t *testing.T) {
	setHome(t)

	p := &Project{
		Name: "legacy", DesiredState: StateRunning, CreatedAt: time.Now().UTC(),
		Owner: LegacyHostOwner,
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != HostOwner() {
		t.Errorf("owner = %q, want the current host owner %q — the fleet stays "+
			"invisible to the dashboard's ownership lens until this matches",
			got.Owner, HostOwner())
	}
	// Persisted, not recomputed per load: the next daemon (and every client) has
	// to see the repaired value.
	again, err := Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if again.Owner != HostOwner() {
		t.Errorf("the re-stamp was not saved: %q", again.Owner)
	}
}

// A TEAMMATE'S island must be left alone. The migration is safe only because the
// legacy value was the default for every install and therefore always meant "the
// host owner"; a real tenant id means someone else, and rewriting it would hand
// their island to the host operator.
func TestATeammatesIslandIsNotRestamped(t *testing.T) {
	setHome(t)

	const teammate = "priya@her-laptop"
	p := &Project{
		Name: "theirs", DesiredState: StateRunning, CreatedAt: time.Now().UTC(),
		Owner: teammate,
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load("theirs")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != teammate {
		t.Errorf("a teammate's island was re-owned to %q — the migration must only "+
			"touch the legacy host-owner literal", got.Owner)
	}
}
