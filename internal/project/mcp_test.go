package project

import (
	"testing"
	"time"
)

// setHome points $HOME at a temp dir so the sidecar lands under an isolated
// ~/.dejima/projects tree.
func setHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestMCPGrants_DenyAllByDefault(t *testing.T) {
	setHome(t)
	grants, err := MCPGrantsFor("isle")
	if err != nil {
		t.Fatalf("MCPGrantsFor: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("a fresh island must be deny-all, got %d grants", len(grants))
	}
	_, ok, err := MCPGrantByServer("isle", "files")
	if err != nil || ok {
		t.Fatalf("no grant expected, got ok=%v err=%v", ok, err)
	}
}

func TestMCPGrants_AddListRemove(t *testing.T) {
	setHome(t)
	now := time.Now().UTC()
	if _, err := AddMCPGrant("isle", MCPGrant{Server: "files", GrantedAt: now}); err != nil {
		t.Fatalf("AddMCPGrant: %v", err)
	}
	if _, err := AddMCPGrant("isle", MCPGrant{Server: "fetch", GrantedAt: now}); err != nil {
		t.Fatalf("AddMCPGrant: %v", err)
	}

	grants, err := MCPGrantsFor("isle")
	if err != nil {
		t.Fatalf("MCPGrantsFor: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("want 2 grants, got %d", len(grants))
	}

	g, ok, err := MCPGrantByServer("isle", "fetch")
	if err != nil || !ok || g.Server != "fetch" {
		t.Fatalf("MCPGrantByServer fetch: ok=%v err=%v g=%+v", ok, err, g)
	}

	removed, ok, err := RemoveMCPGrant("isle", "files")
	if err != nil || !ok || removed.Server != "files" {
		t.Fatalf("RemoveMCPGrant files: ok=%v err=%v removed=%+v", ok, err, removed)
	}
	grants, _ = MCPGrantsFor("isle")
	if len(grants) != 1 || grants[0].Server != "fetch" {
		t.Fatalf("after remove want only fetch, got %+v", grants)
	}
}

func TestMCPGrants_DuplicateRejected(t *testing.T) {
	setHome(t)
	if _, err := AddMCPGrant("isle", MCPGrant{Server: "files"}); err != nil {
		t.Fatalf("AddMCPGrant: %v", err)
	}
	if _, err := AddMCPGrant("isle", MCPGrant{Server: "files"}); err == nil {
		t.Fatal("duplicate grant must be rejected")
	}
}

func TestMCPGrants_RemoveMissing(t *testing.T) {
	setHome(t)
	_, ok, err := RemoveMCPGrant("isle", "ghost")
	if err != nil {
		t.Fatalf("RemoveMCPGrant missing: %v", err)
	}
	if ok {
		t.Fatal("removing a missing grant should report ok=false")
	}
}

// Grants are per-island: one island's grant is invisible to another.
func TestMCPGrants_PerIsland(t *testing.T) {
	setHome(t)
	if _, err := AddMCPGrant("alpha", MCPGrant{Server: "files"}); err != nil {
		t.Fatalf("AddMCPGrant: %v", err)
	}
	_, ok, err := MCPGrantByServer("beta", "files")
	if err != nil || ok {
		t.Fatalf("beta must not see alpha's grant: ok=%v err=%v", ok, err)
	}
}
