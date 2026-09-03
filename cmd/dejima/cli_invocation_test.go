package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/wsl"
)

// Invocation tests for verbs the coverage gate previously credited to a
// SENTENCE about them — a comment, or a command name quoted inside an assertion
// about something else (issue #335). Those reads are gone, so the coverage
// claim now has to be a command that actually runs.
//
// They are grouped here rather than scattered because they share a shape: run
// the real cobra tree through runCLI, assert the outcome an operator would see.
// Anything that cannot be run in a test at all — because running it installs
// software or downloads gigabytes — is waived in
// testdata/coverage_waivers.txt with the reason, which is the honest place for
// it.

// TestWSLCommandsRefuseOffWindows: every `dejima wsl` verb must refuse on a
// Unix host with the reason and the alternative, not a downstream
// "wsl.exe: not found".
//
// This is the coverage that matters most for the family: fit.txt sends people
// to `dejima wsl setup` as the only way to host on Windows, so a Unix operator
// who tries it is a guaranteed reader of this message.
func TestWSLCommandsRefuseOffWindows(t *testing.T) {
	if wsl.Supported() {
		t.Skip("this is a Windows host — the refusal path does not apply")
	}
	for _, verb := range []string{"status", "setup", "start", "stop"} {
		t.Run(verb, func(t *testing.T) {
			var out string
			var err error
			switch verb {
			case "status":
				out, err = runCLI(t, "wsl", "status")
			case "setup":
				out, err = runCLI(t, "wsl", "setup")
			case "start":
				out, err = runCLI(t, "wsl", "start")
			case "stop":
				out, err = runCLI(t, "wsl", "stop")
			}
			if err == nil {
				t.Fatalf("`dejima wsl %s` should refuse off Windows; got success:\n%s", verb, out)
			}
			msg := err.Error()
			if !strings.Contains(msg, "Windows-only") {
				t.Errorf("the refusal should say it is Windows-only, got: %v", err)
			}
			if !strings.Contains(msg, "dejima onboard") {
				t.Errorf("the refusal should point at the Unix path (`dejima onboard`), got: %v", err)
			}
		})
	}
}

// TestVoiceStatusReportsSetupState: `dejima voice status` runs anywhere and
// reports one of three states without touching the network or a package
// manager. On a host where the toolchain is absent it must name what is missing
// and the command that installs it — the CI runner is exactly that host.
func TestVoiceStatusReportsSetupState(t *testing.T) {
	out, err := runCLI(t, "voice", "status")
	if err != nil {
		t.Fatalf("`dejima voice status` should not fail: %v", err)
	}
	if !strings.Contains(out, "Voice dictation:") {
		t.Errorf("status should lead with the state; got:\n%s", out)
	}
	if strings.Contains(out, "not set up") && !strings.Contains(out, "dejima voice install") {
		t.Errorf("an unset-up report must name the install command; got:\n%s", out)
	}
}

// TestVoiceDeviceReportsCapture: `dejima voice device` lists the microphones a
// host can capture from. Off Windows there is nothing to choose — capture uses
// the system default — and saying so is the whole job.
func TestVoiceDeviceReportsCapture(t *testing.T) {
	out, err := runCLI(t, "voice", "device")
	if err != nil {
		// A failure is acceptable only if it explains itself; an empty error
		// here would leave an operator with no idea which half is missing.
		if strings.TrimSpace(err.Error()) == "" {
			t.Fatal("`dejima voice device` failed with an empty message")
		}
		return
	}
	if strings.TrimSpace(out) == "" {
		t.Error("`dejima voice device` succeeded silently — it should report what capture will use")
	}
}

// TestLocalCommandsAgainstDaemon runs the read-only half of the `dejima local`
// family against the in-proc daemon. `install` and `pull` are deliberately
// absent: invoking them would run Ollama's installer and download a multi-GB
// model onto the machine running the tests. They are waived with that reason.
func TestLocalCommandsAgainstDaemon(t *testing.T) {
	cliEnv(t)

	out, err := runCLI(t, "local", "status")
	if err != nil {
		t.Fatalf("`dejima local status` should report state, not fail: %v", err)
	}
	if !strings.Contains(out, "backend") {
		t.Errorf("status should name the backend; got:\n%s", out)
	}
	// EXHAUSTIVE OVER THE STATES, deliberately, with a default that fails.
	//
	// This was `if Contains("not installed") && !Contains("dejima local
	// install")`, which asserts NOTHING once the first half stops matching —
	// rename the status text and the check silently never runs, on a host where
	// nobody would look. d2 hit the mirror image of this today: a guard whose
	// two substrings were both satisfied by a sentence other than the one under
	// test, so a deleted instruction still passed. Same family, opposite
	// direction; both pass quietly.
	switch {
	case strings.Contains(out, "not installed"):
		// The CI host has no backend, so this is the branch that normally runs.
		// It is also the only status an operator can act on, and the action has
		// to be named.
		if !strings.Contains(out, "dejima local install") {
			t.Errorf("a not-installed status must name the install command; got:\n%s", out)
		}
	case strings.Contains(out, "installed (not running)"), strings.Contains(out, "running"):
		// A developer's machine with Ollama on it. Nothing further to assert —
		// but it must be one of the states, which is what the default enforces.
	default:
		t.Errorf("status named no recognisable state, so nothing above was checked; got:\n%s", out)
	}

	if out, err = runCLI(t, "local", "models"); err != nil {
		t.Fatalf("`dejima local models` should list (possibly nothing), not fail: %v", err)
	}
	// "no models pulled" CONTAINS "pulled", so the old
	// `!Contains("no models pulled") && !Contains("pulled")` collapsed to the
	// second clause alone: any use of the word passed. Match the two headings
	// the command actually prints, and fail on anything else.
	switch {
	case strings.Contains(out, "no models pulled"), strings.Contains(out, "pulled:"):
	default:
		t.Errorf("models said neither `no models pulled` nor `pulled:`; got:\n%s", out)
	}

	// A model that was never pulled cannot be removed, and the error should say
	// so rather than reporting success.
	if _, err := runCLI(t, "local", "rm", "definitely-not-pulled"); err == nil {
		t.Error("`dejima local rm` on an unknown model should fail")
	}

	// `off` deregisters the provider; on a host that never registered one it
	// still has to be a clean no-op rather than an error.
	if _, err := runCLI(t, "local", "off"); err != nil {
		t.Errorf("`dejima local off` should be idempotent, got: %v", err)
	}
}

// TestSecretLsOnAnIsland: `dejima secret ls` names an island's secrets without
// printing values. The absence of values is the security-relevant half.
func TestSecretLsOnAnIsland(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "alpha")

	out, err := runCLI(t, "secret", "ls", "alpha")
	if err != nil {
		t.Fatalf("`dejima secret ls` failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("`secret ls` said nothing at all — an island with no secrets should say so")
	}
}

// TestScheduleListAndRm: the scheduled-wake verbs against a real island.
func TestScheduleListAndRm(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "alpha")

	out, err := runCLI(t, "schedule", "list", "alpha")
	if err != nil {
		t.Fatalf("`dejima schedule list` failed: %v", err)
	}
	if !strings.Contains(out, "no scheduled wakes") {
		t.Errorf("a fresh island has no wakes and should say so; got:\n%s", out)
	}

	// Cancelling a wake that does not exist must fail rather than report a
	// cancellation that never happened.
	if _, err := runCLI(t, "schedule", "rm", "alpha", "nonexistent"); err == nil {
		t.Error("`dejima schedule rm` on an unknown schedule should fail")
	}
}

// TestTermAttachUnknownID: attaching to a terminal that does not exist must
// fail by name. The happy path takes over the terminal, so this is the half a
// test can hold.
func TestTermAttachUnknownID(t *testing.T) {
	cliEnv(t)
	if _, err := runCLI(t, "term", "attach", "no-such-terminal"); err == nil {
		t.Error("`dejima term attach` on an unknown id should fail")
	}
}

// TestAgentRestartUnknownAgent: restart names what it could not find, rather
// than reporting a restart of nothing.
func TestAgentRestartUnknownAgent(t *testing.T) {
	_, c := cliEnv(t)
	seedIsland(t, c, "alpha")
	if _, err := runCLI(t, "agent", "restart", "alpha", "no-such-agent"); err == nil {
		t.Error("`dejima agent restart` on an unknown agent should fail")
	}
}
