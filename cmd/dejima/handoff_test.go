package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs f with stdout redirected and returns what it printed.
func capture(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	f()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// The address is printed exactly ONCE otherwise — by setup.sh during step 1 —
// and by the end of the run the operator is several screens of Docker and
// Tailscale output away from it. It is also the only thing from the whole server
// run that has to be carried to ANOTHER MACHINE.
func TestHandoffAddressIsReprintedAtTheEnd(t *testing.T) {
	oldF, oldIP := tailscaleFQDNLookup, tailscaleIPLookup
	t.Cleanup(func() { tailscaleFQDNLookup, tailscaleIPLookup = oldF, oldIP })

	tailscaleFQDNLookup = func() string { return "mac-mini.tailnet1234.ts.net" }
	tailscaleIPLookup = func() (string, bool) { return "", false }

	out := capture(t, printHandoffAddress)
	if !strings.Contains(out, "mac-mini.tailnet1234.ts.net:"+defaultDaemonTCPPort) {
		t.Errorf("the handoff address is not reprinted:\n%s", out)
	}
	if !strings.Contains(out, "DEJIMA_HOST") {
		t.Errorf("never names the variable the operator has to set:\n%s", out)
	}
}

// A raw IP works from this tailnet but is the fragile form for a node-shared
// teammate, whose tailnet may re-address the node. Say so rather than hand it
// over silently.
func TestRawIPIsFlaggedAsLessDurable(t *testing.T) {
	oldF, oldIP := tailscaleFQDNLookup, tailscaleIPLookup
	t.Cleanup(func() { tailscaleFQDNLookup, tailscaleIPLookup = oldF, oldIP })

	tailscaleFQDNLookup = func() string { return "" }
	tailscaleIPLookup = func() (string, bool) { return "100.101.102.103", true }

	out := capture(t, printHandoffAddress)
	if !strings.Contains(out, "100.101.102.103:"+defaultDaemonTCPPort) {
		t.Errorf("the address is missing:\n%s", out)
	}
	if !strings.Contains(out, "MagicDNS") {
		t.Errorf("hands over a raw IP without noting it is the fragile form:\n%s", out)
	}
}

// NO TAILSCALE MUST NOT BE SILENT. Nothing is broken, and saying so is the
// difference between "my setup failed" and "this only affects reaching it from
// elsewhere" — which is the reading an operator takes from an empty screen.
func TestNoTailnetSaysNothingIsBroken(t *testing.T) {
	oldF, oldIP := tailscaleFQDNLookup, tailscaleIPLookup
	t.Cleanup(func() { tailscaleFQDNLookup, tailscaleIPLookup = oldF, oldIP })

	tailscaleFQDNLookup = func() string { return "" }
	tailscaleIPLookup = func() (string, bool) { return "", false }

	out := capture(t, printHandoffAddress)
	if strings.TrimSpace(out) == "" {
		t.Fatal("printed nothing; an empty screen reads as a failed setup")
	}
	if !strings.Contains(out, "LOCALLY") {
		t.Errorf("does not say the install itself is fine:\n%s", out)
	}
	if !strings.Contains(out, "dejima doctor") {
		t.Errorf("does not name the recovery — not knowing how to get the address back "+
			"is the actual failure, not missing it:\n%s", out)
	}
}

// THE CALL SITE, not just the function. A mutation deleting the reprint from
// markSetupDoneIfHealthy passed against the tests above, because they drive
// printHandoffAddress directly — the logic was covered and the wiring was not.
func TestSuccessfulSetupReprintsTheAddress(t *testing.T) {
	oldF, oldIP, oldH := tailscaleFQDNLookup, tailscaleIPLookup, daemonHealthyFn
	t.Cleanup(func() {
		tailscaleFQDNLookup, tailscaleIPLookup, daemonHealthyFn = oldF, oldIP, oldH
	})
	tailscaleFQDNLookup = func() string { return "mac-mini.tailnet1234.ts.net" }
	tailscaleIPLookup = func() (string, bool) { return "", false }
	daemonHealthyFn = func(context.Context) bool { return true }

	// Keep the dismissal marker out of the operator's real config.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_HOME", t.TempDir())

	var ok bool
	out := capture(t, func() { ok = markSetupDoneIfHealthy(context.Background()) })
	if !ok {
		t.Fatal("a healthy daemon did not verify")
	}
	if !strings.Contains(out, "mac-mini.tailnet1234.ts.net") {
		t.Errorf("setup verified without reprinting the handoff address — the operator is "+
			"left to scroll back through Docker and Tailscale output for it:\n%s", out)
	}
}
