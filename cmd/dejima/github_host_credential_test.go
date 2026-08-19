package main

import (
	"os"
	"path/filepath"
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

// GH_TOKEN silently outranks the island's mounted GitHub credential, swapping
// which identity — and which permissions — every clone/push uses, with no
// error. Setting it is the moment that becomes true, so it's the moment to say
// so.
func TestSecretSetWarnsOnGitHubTokenPrecedence(t *testing.T) {
	_, c := cliEnv(t)
	seedHostGHConfig(t)
	seedIsland(t, c, "proj")
	if _, err := runCLI(t, "github", "host-credential", "grant", "proj"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	withOSStdin(t, "ghp_value")
	out, err := runCLI(t, "secret", "set", "proj", "GH_TOKEN", "--stdin")
	if err != nil {
		t.Fatalf("secret set: %v", err)
	}
	if !strings.Contains(out, "OVERRIDES") {
		t.Errorf("setting GH_TOKEN over a GitHub credential must warn:\n%s", out)
	}
	if !strings.Contains(out, "permissions") {
		t.Errorf("the warning should name the consequence, not just the fact:\n%s", out)
	}
}

// An ordinary secret must not drag the GitHub warning along — a warning that
// fires on everything is one nobody reads.
func TestSecretSetQuietForUnrelatedNames(t *testing.T) {
	_, c := cliEnv(t)
	seedHostGHConfig(t)
	seedIsland(t, c, "proj")
	if _, err := runCLI(t, "github", "host-credential", "grant", "proj"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	withOSStdin(t, "npm_value")
	out, err := runCLI(t, "secret", "set", "proj", "NPM_TOKEN", "--stdin")
	if err != nil {
		t.Fatalf("secret set: %v", err)
	}
	if strings.Contains(out, "OVERRIDES") {
		t.Errorf("NPM_TOKEN has nothing to do with gh:\n%s", out)
	}
}

// withOSStdin points os.Stdin at a file holding value for the duration of the
// test — `secret set --stdin` reads os.Stdin directly.
func withOSStdin(t *testing.T, value string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(value); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = prev; f.Close() })
}

// seedHostGHConfig creates the host operator's ~/.config/gh under the test HOME.
// Without it the grant resolves to no mount at all (credentialBindMounts skips a
// missing dir), so an island would have no GitHub credential for GH_TOKEN to
// override — a fixture that quietly tests nothing.
func seedHostGHConfig(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "gh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  oauth_token: ghp_host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
