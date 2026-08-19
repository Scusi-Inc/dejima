package main

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/aoos/dejima/internal/secrets"
)

// The CLI table tests run a real in-proc daemon, so anything that stores a
// secret goes through the real platform store unless something stops it. On
// macOS that means `security` against the login keychain — which either prompts
// the developer or, on a locked CI keychain, blocks inside the HTTP handler
// until the test binary panics at ten minutes.
//
// isolateSecretsBackend exists to prevent that, and these tests exist because
// setting an environment variable is precisely the kind of guard that can stop
// working without anything going red: rename the constant, change how
// osKeychain reads it, add a third cliEnv-alike that forgets the line, and the
// dependency comes back silently on the one platform CI is slowest to tell you
// about.

// stubKeychainCLI puts an executable with the platform keychain tool's name at
// the front of PATH.
//
// Without this the test proves nothing off macOS: a Linux box with no
// `secret-tool` installed selects the file backend anyway, so the assertion
// below would pass with the guard deleted — a check that silently doesn't run,
// which is the failure mode it was written to catch. Stubbing the tool makes
// the keychain backend genuinely selectable, so "file" can only be the guard's
// doing.
//
// The stub is never executed: osKeychain decides on exec.LookPath alone.
func stubKeychainCLI(t *testing.T) {
	t.Helper()
	name := "secret-tool"
	if goruntime.GOOS == "darwin" {
		name = "security"
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, name)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write keychain stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The guard must actually take effect — not merely be set. This asserts through
// the real Store, so it fails if BackendEnvVar stops being honored, rather than
// re-reading the variable we just wrote (which would pass no matter what the
// package does with it).
func TestCLIEnvForcesTheFileSecretsBackend(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		t.Skip("no platform keychain backend on " + goruntime.GOOS)
	}
	stubKeychainCLI(t)
	cliEnv(t)
	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if got := st.Backend(); got != "file" {
		t.Errorf("cliEnv left the secrets backend as %q; CLI tests must not touch the machine's keychain "+
			"(it prompts on a developer's Mac and hangs on a locked CI keychain)", got)
	}
}

// cliEnvFull is a separate constructor and has to carry the same guard. Testing
// it separately is the point: the flake reached CI through a helper that looked
// like the one that was already safe.
func TestCLIEnvFullForcesTheFileSecretsBackend(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		t.Skip("no platform keychain backend on " + goruntime.GOOS)
	}
	stubKeychainCLI(t)
	cliEnvFull(t)
	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if got := st.Backend(); got != "file" {
		t.Errorf("cliEnvFull left the secrets backend as %q; see TestCLIEnvForcesTheFileSecretsBackend", got)
	}
}

// The negative control for the two tests above: with the stub on PATH and no
// guard, the keychain backend IS what you get. If this ever reports "file" the
// stub has stopped working and the guard tests have gone hollow — they would
// still pass, and they would no longer mean anything.
func TestKeychainStubMakesTheKeychainBackendReachable(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		t.Skip("no platform keychain backend on " + goruntime.GOOS)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secrets.BackendEnvVar, "") // no guard
	stubKeychainCLI(t)
	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if st.Backend() == "file" {
		t.Error("the keychain stub is not being found, so the isolation tests above prove nothing — " +
			"they would pass with isolateSecretsBackend deleted")
	}
}
