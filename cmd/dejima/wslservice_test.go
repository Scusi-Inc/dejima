package main

import (
	"strings"
	"testing"
)

// Every assertion here is a failure that happened on a real machine first, in
// this order: PATH, then HOME, then the stale socket. They share one cause —
// THE UNATTENDED CONTEXT LACKS WHAT EVERY INTERACTIVE CONTEXT SUPPLIES FOR FREE
// — which is why none of them could be found by running the command by hand.
func TestDejimadUnitCarriesWhatTheBootContextLacks(t *testing.T) {
	// HOME. systemd system services get almost no environment, and without this
	// the daemon exits instantly with
	//   err="locate home dir: $HOME is not defined"
	// restarting every two seconds forever.
	if !strings.Contains(dejimadUnit, "Environment=HOME=/root") {
		t.Error("unit does not set HOME; dejimad exits with 'locate home dir: $HOME is not defined'")
	}

	// Absolute path. The boot PATH excludes /usr/local/bin.
	if !strings.Contains(dejimadUnit, "/usr/local/bin/dejimad") {
		t.Error("unit invokes dejimad without an absolute path; the unattended PATH will not find it")
	}

	// The stale socket. dejimad refuses to bind over one, so an unclean shutdown
	// would leave the unit failing permanently.
	if !strings.Contains(dejimadUnit, "dejimad.sock") {
		t.Error("unit never clears a stale socket; one unclean shutdown makes it fail forever")
	}
	// ...but only when nothing is running, or it would delete a live daemon's socket.
	if !strings.Contains(dejimadUnit, "pgrep -x dejimad") {
		t.Error("the stale-socket clear is unguarded — it could remove the socket of a running daemon")
	}

	// Restart=always is why systemd is preferred over a boot command: it heals,
	// and it gives the self-updater a `systemctl restart` that works.
	if !strings.Contains(dejimadUnit, "Restart=always") {
		t.Error("unit does not restart on failure, which is the reason to use systemd at all")
	}

	// Enabled for boot, not merely startable.
	if !strings.Contains(dejimadUnit, "WantedBy=multi-user.target") {
		t.Error("unit has no [Install] target, so `systemctl enable` cannot make it start at boot")
	}
}

// The unit is written through base64 and must survive the round trip byte for
// byte: it crosses PowerShell, wsl.exe and sh, and it contains quotes,
// redirections and single-quoted sh fragments. Hand-quoting this is how a
// command gets evaluated by the wrong shell — which happened to the operator on
// this exact file, PowerShell swallowing a `>>` and writing to a Windows path.
func TestUnitSurvivesBase64RoundTrip(t *testing.T) {
	enc := b64(dejimadUnit)
	got, err := unb64(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != dejimadUnit {
		t.Errorf("round trip changed the unit:\n--- want ---\n%s\n--- got ---\n%s", dejimadUnit, got)
	}
	// Nothing in the encoded blob may be reinterpreted by an intermediate shell.
	for _, r := range enc {
		switch r {
		case '"', '\'', '`', '$', '>', '<', '|', '&', ';', '\n':
			t.Errorf("encoded unit contains %q, which a shell on the way through can reinterpret", r)
		}
	}
}
