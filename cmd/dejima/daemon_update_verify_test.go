package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// THE SUCCESS PATH OF A DAEMON UPDATE LOOKS LIKE A NETWORK FAILURE. The daemon
// replaces its binary and restarts — which is the point — and the connection
// carrying the response dies as a direct consequence. On WSL every connection is
// a wsl.exe + socat subprocess, so the tunnel goes with it.
//
// The operator saw "dejimad unavailable" and "daemon update failed" while
// `dejimad --version` reported the NEW version. The update had worked.
func TestDaemonUpdateVerdictFromTheVersion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		msg        daemonUpdateVerifyMsg
		wantBanner bool   // green "updated"
		wantErrHas string // sticky error must contain
	}{
		{
			name:       "came back on the new version — the transport error was the restart",
			msg:        daemonUpdateVerifyMsg{version: "v0.8.94", want: "v0.8.94"},
			wantBanner: true,
		},
		{
			name:       "came back on the OLD version — a real failure, named precisely",
			msg:        daemonUpdateVerifyMsg{version: "v0.8.93", want: "v0.8.94"},
			wantErrHas: "still on v0.8.93",
		},
		{
			name:       "never came back — a different fault with a different remedy",
			msg:        daemonUpdateVerifyMsg{want: "v0.8.94", err: errDaemonNeverReturned},
			wantErrHas: "did not come back",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m tuiModel
			m.width, m.height = 100, 40
			m.updating = "daemon restarting — checking the new version…"
			m.daemonUpdate = true

			upd, cmd := m.Update(tc.msg)
			mm := upd.(tuiModel)

			if mm.updating != "" {
				t.Errorf("left the in-progress banner up: %q", mm.updating)
			}
			if tc.wantBanner {
				if cmd == nil {
					t.Error("no success banner for an update that demonstrably worked")
				}
				if mm.daemonUpdate {
					t.Error("still offering the update it just confirmed applied")
				}
				if mm.updateError != "" {
					t.Errorf("reported an error for a successful update: %q", mm.updateError)
				}
				return
			}
			if !strings.Contains(mm.updateError, tc.wantErrHas) {
				t.Errorf("error %q does not contain %q", mm.updateError, tc.wantErrHas)
			}
		})
	}
}

// A transport error from the update call must NOT be reported as failure. It
// must trigger verification, because that error is the expected consequence of
// the restart the update requested.
func TestTransportErrorTriggersVerificationNotFailure(t *testing.T) {
	var m tuiModel
	m.width, m.height = 100, 40
	m.latestRelease = "v0.8.94"

	upd, cmd := m.Update(daemonUpdatedMsg{err: errors.New("daemon unreachable: EOF")})
	mm := upd.(tuiModel)

	if mm.updateError != "" {
		t.Errorf("declared failure on a transport error — that error IS the restart: %q", mm.updateError)
	}
	if cmd == nil {
		t.Error("no verification scheduled, so the operator is left with neither a result nor a check")
	}
	if !strings.Contains(mm.updating, "restarting") {
		t.Errorf("says nothing about what is happening: %q", mm.updating)
	}
}

// A genuine refusal (deferred, terminals attached) must still be handled as
// before — this change must not swallow the cases that were already correct.
func TestDeferredUpdateStillPrompts(t *testing.T) {
	var m tuiModel
	m.width, m.height = 100, 40
	upd, _ := m.Update(daemonUpdatedMsg{resp: &api.AdminUpdateResponse{Deferred: true, AttachedClients: 2}})
	mm := upd.(tuiModel)
	if mm.confirm == nil {
		t.Error("a deferred update no longer re-prompts to force")
	}
}
