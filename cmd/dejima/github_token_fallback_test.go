package main

import (
	"errors"
	"testing"
)

// deviceFlowUnconfigured matches the daemon's refusal by message text, so the
// two must not drift. A self-hosted daemon has no OAuth app, which makes this
// the DEFAULT path rather than an edge case: if the match breaks, every new
// operator gets the dead-end exit 1 again.
func TestDeviceFlowUnconfiguredDetection(t *testing.T) {
	// The daemon's refusal, up to the part we match on. NB: deliberately does not
	// include the trailing `dejima <cmd>` the real message carries — the coverage
	// gate scans test files for command tokens and would read one here as "that
	// command is now tested" (same trap noted in clone_hint_test.go).
	daemon := errors.New("guided GitHub sign-in isn't configured on this daemon (no OAuth app); use the token path instead")
	if !deviceFlowUnconfigured(daemon) {
		t.Error("did not recognise the daemon's 'not configured' refusal — the token fallback would never trigger")
	}

	for _, other := range []error{
		nil,
		errors.New("unknown github identity \"bob\""),
		errors.New("http 503"),
		errors.New("authorization_pending"),
	} {
		if deviceFlowUnconfigured(other) {
			t.Errorf("misread %v as 'guided sign-in not configured'", other)
		}
	}
}

// The token ladder must not require the gh CLI. A self-hosted daemon has no
// OAuth app, so guided sign-in is dark; if the only remaining route also needs
// gh, a host without it cannot connect GitHub at all — which is exactly where
// an operator landed: a window that exited 1 saying "gh CLI not found".
func TestConnectCommandExposesTokenFlags(t *testing.T) {
	connect := newGithubConnectCmd()
	for _, name := range []string{"token", "token-stdin"} {
		if connect.Flags().Lookup(name) == nil {
			t.Errorf("`github connect` is missing --%s; a host without gh has no way to supply a token", name)
		}
	}
}
