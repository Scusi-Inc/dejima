package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// "Applying" must not be reported as "updated".
//
// The daemon installs the new binary synchronously, acks with Applying=true, and
// THEN restarts — it has to ack first, because the restart kills the process
// serving the response. So Applying means "installed, restart requested". It is
// a statement about what is about to happen.
//
// The TUI rendered it as a green "daemon updated to v0.9.3 — restarting,
// reconnecting shortly", after which the banner came back reading "update
// available: daemon v0.8.96→v0.9.3". The install had worked; the restart had
// not. On WSL there is often no systemd to restart into — which the daemon
// already knows and writes to its own log, where nobody was looking, because the
// screen said it had worked.
func TestApplyingIsNotReportedAsUpdated(t *testing.T) {
	m := tuiModel{activeHost: "wsl://dejima", daemonUpdate: true}
	out, cmd := m.Update(daemonUpdatedMsg{resp: &api.AdminUpdateResponse{
		Applying: true, Latest: "v0.9.3",
	}})
	got := out.(tuiModel)

	if got.updateApplied != "" {
		t.Errorf("the TUI claims the update is done off the ACK alone: %q", got.updateApplied)
	}
	if got.updating == "" {
		t.Error("nothing on screen says the update is still in progress, so the " +
			"operator has no reason to wait for the real answer")
	}
	if cmd == nil {
		t.Fatal("no verification was scheduled — the version the daemon is actually " +
			"running is never checked, which is how a failed restart reads as success")
	}
	// The banner must stay until the new version is CONFIRMED. Clearing it here
	// is what made the update look done and then reappear.
	if !got.daemonUpdate {
		t.Error("the update-available banner was cleared before the daemon was " +
			"confirmed on the new version")
	}
}

// Confirmed on the new version is the only success.
func TestOnlyAConfirmedVersionCountsAsUpdated(t *testing.T) {
	m := tuiModel{activeHost: "wsl://dejima", daemonUpdate: true, updating: "checking…"}
	out, _ := m.Update(daemonUpdateVerifyMsg{version: "v0.9.3", want: "v0.9.3"})
	got := out.(tuiModel)
	if got.updateApplied == "" {
		t.Error("a daemon confirmed on the wanted version is not reported as updated")
	}
	if got.daemonUpdate {
		t.Error("the update-available banner survived a confirmed update")
	}
}

// Back on the OLD version must name the restart, not the update.
//
// The new binary is already installed — prepareDaemonUpdate ran synchronously
// and only then did the daemon ack. So "the update did not take" reads as "try
// again", and trying again re-installs a binary that is already there. The
// remedy is to restart the process, and on WSL that is not systemctl.
func TestBackOnTheOldVersionNamesTheRestart(t *testing.T) {
	m := tuiModel{activeHost: "wsl://dejima", daemonUpdate: true, updating: "checking…"}
	out, _ := m.Update(daemonUpdateVerifyMsg{version: "v0.8.96", want: "v0.9.3"})
	got := out.(tuiModel)
	if got.updateApplied != "" {
		t.Errorf("a daemon still on the old version was reported as updated: %q", got.updateApplied)
	}
	if got.updateError == "" {
		t.Fatal("no error at all for an update that did not take")
	}
	if !strings.Contains(got.updateError, "v0.8.96") {
		t.Errorf("the error does not say which version it is still on: %q", got.updateError)
	}
	// The WSL remedy, because systemctl is a circle on a distro without systemd.
	if !strings.Contains(got.updateError, "pkill -x dejimad") ||
		!strings.Contains(got.updateError, "dejima wsl start") {
		t.Errorf("a WSL host is not told how to restart its daemon — `systemctl "+
			"restart` cannot work on a distro with no systemd: %q", got.updateError)
	}
	if !strings.Contains(got.updateError, "installed") {
		t.Errorf("the message does not say the binary is already installed, so it "+
			"reads as \"try updating again\": %q", got.updateError)
	}
}

// A non-WSL host gets the generic remedy, not WSL commands.
func TestANonWSLHostIsNotToldToRunWSLCommands(t *testing.T) {
	m := tuiModel{activeHost: "mac-mini:7273", daemonUpdate: true}
	out, _ := m.Update(daemonUpdateVerifyMsg{version: "v0.8.96", want: "v0.9.3"})
	got := out.(tuiModel)
	if strings.Contains(got.updateError, "wsl") {
		t.Errorf("a remote non-WSL host was told to run wsl commands: %q", got.updateError)
	}
}
