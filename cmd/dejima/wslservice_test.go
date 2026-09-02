package main

import (
	"os"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/srcscan"
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

// A NATIVE docker engine cannot reach the host's loopback from a container, so
// the listeners have to move onto the bridge. The operator's first island failed
// with exactly this:
//
//	Failed to connect to host.docker.internal port 7280 after 0 ms
//
// Docker Desktop and colima hide it because a VM forwards the name to loopback.
// WSL installs a native engine, so there is no VM and no forwarding.
func TestUnitMovesListenersOntoTheBridge(t *testing.T) {
	got := dejimadUnitFor("172.17.0.1")

	for _, want := range []string{
		"Environment=HOME=/root",
		"Environment=DEJIMAD_EGRESS_PROXY=172.17.0.1:7280",
		"Environment=DEJIMAD_TOKEN_TCP=172.17.0.1:7274",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unit is missing %q:\n%s", want, got)
		}
	}
	// HOME must survive: without it the daemon exits instantly with
	// "locate home dir: $HOME is not defined".
	if strings.Count(got, "Environment=HOME=/root") != 1 {
		t.Errorf("HOME appears %d times, want exactly 1", strings.Count(got, "Environment=HOME=/root"))
	}
	// Never a wildcard. assertHostInternalBind refuses those, and rightly.
	for _, bad := range []string{"0.0.0.0", "::"} {
		if strings.Contains(got, bad) {
			t.Errorf("unit binds a wildcard (%q), which the daemon refuses and which would "+
				"expose the autonomy listener to the LAN:\n%s", bad, got)
		}
	}
}

// No gateway detected: omit the overrides rather than guess. A wrong bind is
// worse than today's behaviour, and setup says what it could not determine.
func TestUnitOmitsOverridesWhenNoGatewayFound(t *testing.T) {
	got := dejimadUnitFor("")
	if strings.Contains(got, "DEJIMAD_EGRESS_PROXY") || strings.Contains(got, "DEJIMAD_TOKEN_TCP") {
		t.Errorf("invented a listener address with no gateway detected:\n%s", got)
	}
	if !strings.Contains(got, "Environment=HOME=/root") {
		t.Errorf("dropped HOME while omitting the overrides:\n%s", got)
	}
}

// Rewriting the unit must RESTART the daemon. `systemctl enable --now` starts a
// STOPPED unit and leaves a RUNNING one alone — so on a machine where setup had
// already run, rewriting the file would change the config on disk and nothing
// else. The daemon would keep its old listeners while setup reported success,
// which is the exact "reports success, isn't true" shape this whole WSL
// investigation was about.
func TestUnitRewriteRestartsTheDaemon(t *testing.T) {
	// The install script is built inline in ensureWSLDaemonSupervision; assert on
	// the command sequence it must contain. This is a source-level check because
	// the property is "the restart step exists", and its absence has no runtime
	// signature on a machine with no systemd.
	body := codeOf(t)
	if !strings.Contains(body, "systemctl restart dejimad") {
		t.Error("the unit is written without restarting the daemon; a rewritten unit " +
			"would never take effect on an already-running daemon")
	}
	if !strings.Contains(body, "systemctl daemon-reload") {
		t.Error("systemd is never told to re-read the unit file")
	}
}

// Detection must ask DOCKER, not only the OS interface. On a fresh distro
// docker0 does not exist until dockerd has started, and setup probes moments
// after installing it — so an interface-only probe returns nothing exactly when
// it matters, and the operator's first clone on a clean machine fails with
// "Failed to connect to host.docker.internal port 7280".
func TestGatewayDetectionAsksDockerNotJustTheInterface(t *testing.T) {
	body := codeOf(t)
	if !strings.Contains(body, "docker network inspect bridge") {
		t.Error("detection never asks Docker, so it cannot answer before docker0 exists " +
			"or on an image without iproute2")
	}
	// And it must still try the interface, for engines where the docker CLI is
	// unavailable to this user.
	if !strings.Contains(body, "ip -4 -o addr show docker0") {
		t.Error("dropped the interface probe; it is the fallback when the docker CLI is not usable")
	}
	// A retry, because dockerd coming up is the race that was lost.
	if !strings.Contains(body, "attempt < 5") {
		t.Error("no retry — detection races dockerd's startup and loses on a fresh distro")
	}
}

// codeOf returns wslservice.go with its COMMENTS STRIPPED, so the guards below
// cannot be satisfied by the prose that explains the code.
//
// This is not hypothetical here: a mutation deleting the `docker network
// inspect bridge` probe PASSED, because the doc comment above it names the same
// command. That is the fourth-plus instance of a comment satisfying a
// source-level guard in this repo, which is why internal/srcscan exists.
func codeOf(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("wslservice.go")
	if err != nil {
		t.Fatal(err)
	}
	out, ok := srcscan.StripGoComments(string(src))
	if !ok {
		// A stripper that cannot parse must not hand back the original and let
		// the guard match prose again.
		t.Fatal("could not strip comments; these guards cannot run safely on raw source")
	}
	return out
}

// Supervision must be installed BEFORE the "daemon is already running" early
// return, or it never runs on the machine that most needs it: one already set
// up once.
//
// The operator re-ran `dejima wsl setup` three times against a healthy daemon
// and got a clean report every time — socat present, Docker present, dejimad
// running, image built, connection verified — while the unit was never touched
// and the listener overrides never arrived. Nothing on screen suggested a step
// had been skipped, because from setup's point of view none had been.
//
// Installing supervision is about the NEXT restart. Whether the daemon happens
// to be up right now has nothing to do with whether its unit is correct.
func TestSupervisionIsInstalledBeforeTheAlreadyRunningReturn(t *testing.T) {
	src, err := os.ReadFile("wsl.go")
	if err != nil {
		t.Fatal(err)
	}
	code, ok := srcscan.StripGoComments(string(src))
	if !ok {
		t.Fatal("could not strip comments; this guard cannot run safely on raw source")
	}
	start := strings.Index(code, "func startDaemonInWSL")
	if start < 0 {
		// A guard that cannot find what it checks must fail, not pass.
		t.Fatal("startDaemonInWSL not found — this guard can no longer see what it checks")
	}
	body := code[start:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	sup := strings.Index(body, "ensureWSLDaemonSupervision")
	// A CODE marker, not a comment one. The first version searched for the
	// phrase "already up", which exists only in the comment on that return —
	// and since this guard strips comments first, it found nothing and SKIPPED.
	// A skip reports ok. The mutation putting the bug back passed against it.
	early := strings.Index(body, "pgrep -x dejimad")
	if sup < 0 {
		t.Fatal("startDaemonInWSL never installs supervision")
	}
	if early < 0 {
		t.Fatal("the already-running probe is gone; this guard can no longer see what it checks")
	}
	if sup > early {
		t.Error("supervision is installed AFTER the already-running early return, so a host " +
			"whose daemon is up never gets its unit corrected — setup reports a clean run " +
			"and changes nothing")
	}
}
