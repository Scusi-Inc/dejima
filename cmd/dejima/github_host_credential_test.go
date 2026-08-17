package main

import (
	"strings"
	"testing"
)

// The default an operator sees: a fresh island holds no host GitHub credential,
// and `status` says so plainly rather than being silent about it.
func TestGithubHostCredentialStatusDefaultsToDenied(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	out, err := runCLI(t, "github", "host-credential", "status", "proj")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "DENIED") {
		t.Errorf("a fresh island should report DENIED, got: %q", out)
	}
}

// Grant → status reflects it. And the output must say the grant isn't live
// until the container is recreated, because the credential is a bind mount —
// without that line the operator grants, sees no change, and re-grants.
func TestGithubHostCredentialGrantThenStatus(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	out, err := runCLI(t, "github", "host-credential", "grant", "proj")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !strings.Contains(out, "granted") {
		t.Errorf("grant should confirm, got: %q", out)
	}
	if !strings.Contains(out, "next created") {
		t.Errorf("grant must say it takes effect on container recreate, got: %q", out)
	}

	out, err = runCLI(t, "github", "host-credential", "status", "proj")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "GRANTED") {
		t.Errorf("status after grant should report GRANTED, got: %q", out)
	}
}

// Revoke works through the CLI, and revoking twice surfaces the daemon's 404
// rather than pretending it succeeded.
func TestGithubHostCredentialRevoke(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	if _, err := runCLI(t, "github", "host-credential", "grant", "proj"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	out, err := runCLI(t, "github", "host-credential", "revoke", "proj")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !strings.Contains(out, "revoked") {
		t.Errorf("revoke should confirm, got: %q", out)
	}
	if _, err := runCLI(t, "github", "host-credential", "revoke", "proj"); err == nil {
		t.Error("revoking twice should fail — there was nothing to revoke")
	}
}

// The alias exists because "host-credential" is a mouthful to type at a prompt
// while auditing a fleet.
func TestGithubHostCredentialAlias(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "proj")

	if _, err := runCLI(t, "github", "host-cred", "status", "proj"); err != nil {
		t.Fatalf("host-cred alias should work: %v", err)
	}
}

// An unknown island is a clean error, not a panic or a misleading "DENIED"
// (which would read as "this island exists and is safe").
func TestGithubHostCredentialUnknownIsland(t *testing.T) {
	_, _ = cliEnv(t)
	out, err := runCLI(t, "github", "host-credential", "status", "nope")
	if err == nil {
		t.Fatalf("status for a missing island should fail, got output: %q", out)
	}
	if strings.Contains(out, "DENIED") {
		t.Errorf("a missing island must not be reported as a denied one: %q", out)
	}
}
