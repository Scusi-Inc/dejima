package main

import (
	"strings"
	"testing"
)

// The first-run question must offer all THREE destinations at once.
//
// It used to ask "set up here, or join?" and then, on a fresh Mac, a second
// question offering "provision this host" or "just the generic setup
// walkthrough". Between them those cover local, host and client — but only two
// were ever on screen together, and LOCAL was wearing the label "generic setup
// walkthrough", which reads as a fallback rather than a choice.
//
// So an operator who wanted a daemon on her own Mac chose "set up here", landed
// in host provisioning, and got a Tailscale check she had no use for — which
// reconnected the remote profile she was trying to leave. Reported as two
// options where there should have been three.
func TestFirstRunOffersLocalHostClient(t *testing.T) {
	src := onboardSourceForTest(t)

	for _, want := range []string{"l) Local", "h) Host", "c) Client"} {
		if !strings.Contains(src, want) {
			t.Errorf("the first-run choice does not offer %q", want)
		}
	}
	// LOCAL FIRST: it is the common case and installs the least. Host commits the
	// machine to always-on; client needs an invite the user may not have.
	li := strings.Index(src, "l) Local")
	hi := strings.Index(src, "h) Host")
	ci := strings.Index(src, "c) Client")
	if li >= hi || hi >= ci {
		t.Errorf("order is local=%d host=%d client=%d, want local before host before client", li, hi, ci)
	}
	if !strings.Contains(src, "Choice [l/h/c/n/N]") {
		t.Error("the prompt key list does not match the options offered")
	}
}

// Local must not quietly mean host. `s`/`y` were the old "set up here" keys and
// now land on LOCAL — the least destructive of the two, and the one the second
// question used to route most people toward anyway.
func TestLegacySetUpKeysLandOnLocalNotHost(t *testing.T) {
	src := onboardSourceForTest(t)
	localArm := arm(src, `case "l", "L", "local",`)
	if !strings.Contains(localArm, `"s"`) || !strings.Contains(localArm, `"y"`) {
		t.Errorf("legacy set-up keys do not land on local:\n%s", localArm)
	}
	hostArm := arm(src, `case "h", "H", "host":`)
	if strings.Contains(hostArm, `"s"`) || strings.Contains(hostArm, `"y"`) {
		t.Errorf("a legacy set-up key routes to HOST provisioning, which installs "+
			"never-sleep settings and Tailscale nobody asked for:\n%s", hostArm)
	}
}

// Windows cannot BE a host — dejimad needs a Unix host with Docker — so picking
// Host there must say so and offer WSL2, not walk into a flow that cannot
// finish.
func TestHostOnWindowsRedirectsToWSL(t *testing.T) {
	fn := arm(onboardSourceForTest(t), "func firstRunSetUpHost(")
	if !strings.Contains(fn, "firstRunWindowsClient") {
		t.Error("host provisioning does not special-case Windows, which cannot run the daemon at all")
	}
	if !strings.Contains(fn, "firstRunSetUpWSL") {
		t.Error("Windows host selection does not offer the WSL2 path that IS achievable")
	}
}
