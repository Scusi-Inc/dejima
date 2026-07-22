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
	// Verbatim from internal/api/github_device_flow.go.
	daemon := errors.New("guided GitHub sign-in isn't configured on this daemon (no OAuth app); use `dejima auth push --github` with a token instead")
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
