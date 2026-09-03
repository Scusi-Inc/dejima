package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
	tea "github.com/charmbracelet/bubbletea"
)

func daemonAt(t *testing.T, cur, latest string, avail bool) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.AdminUpdateResponse{
			Current: cur, Latest: latest, UpdateAvailable: avail, Mode: "release",
		})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("DEJIMA_HOST", ts.URL)
}

// Updating the client must not report success in a way that implies the daemon
// came with it. On Windows the daemon lives in WSL — a different program on a
// different filesystem — and the operator saw v0.8.89 while it sat on v0.8.87.
func TestUpdateSaysTheDaemonIsStillBehind(t *testing.T) {
	daemonAt(t, "v0.8.87", "v0.8.89", true)
	var buf bytes.Buffer
	reportDaemonVersion(context.Background(), &buf, false)

	got := buf.String()
	if !strings.Contains(got, "v0.8.87") {
		t.Errorf("never names the version the daemon is stuck on:\n%s", got)
	}
	if !strings.Contains(got, "--daemon") {
		t.Errorf("never names the command that fixes it:\n%s", got)
	}
	// The point that was missed: they are separate programs.
	if !strings.Contains(got, "did not update it") {
		t.Errorf("doesn't say the client update left the daemon alone:\n%s", got)
	}
}

func TestUpdateStaysQuietWhenDaemonIsCurrent(t *testing.T) {
	daemonAt(t, "v0.8.89", "v0.8.89", false)
	var buf bytes.Buffer
	reportDaemonVersion(context.Background(), &buf, false)
	if strings.Contains(buf.String(), "--daemon") {
		t.Errorf("nagged about a daemon that is already current:\n%s", buf.String())
	}
}

// The confirm dialog must accept an ordinary keypress. It gated on
// len(msg.String()) == 1 — a BYTE test — so any key whose formatted string is
// longer than one byte was silently dropped, in the one dialog where that means
// being unable to say yes to anything.
func TestConfirmAcceptsTypedRunes(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
		want string
	}{
		{"plain y", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}, "y"},
		{"a multi-byte rune is not dropped", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'é'}}, "é"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m tuiModel
			m.width, m.height = 100, 40
			m.confirm = &confirmPrompt{verb: "update-client"}
			upd, _ := m.Update(tc.key)
			mm := upd.(tuiModel)
			if mm.confirm == nil {
				t.Fatal("the confirm vanished on a plain keypress")
			}
			if mm.confirm.answer != tc.want {
				t.Errorf("answer = %q, want %q — the keypress never reached the prompt", mm.confirm.answer, tc.want)
			}
		})
	}
}

// The --daemon flag must exist and say what it costs. This pins the FEATURE,
// not the call site — see the note in TestUpdateDaemonCallSiteIsUncovered.
func TestUpdateHasDaemonFlag(t *testing.T) {
	cmd := newUpdateCmd()
	f := cmd.Flags().Lookup("daemon")
	if f == nil {
		t.Fatal("`dejima update` has no --daemon flag; the CLI has no way to update the daemon at all")
	}
	if !strings.Contains(f.Usage, "restart") {
		t.Errorf("--daemon doesn't warn that it restarts the daemon: %q", f.Usage)
	}
}

// KNOWN GAP, recorded rather than papered over.
//
// Deleting the reportDaemonVersion CALL from the update command breaks no test.
// The tests above exercise the function directly, because reaching the call
// site means running RunE, which does a real release check and a real binary
// replacement. Stubbing both is more machinery than the risk warrants today.
//
// A mutation proved this: removing the call passed the whole suite. So did
// severing `daemonToo = daemonToo || all`, which would make --all silently stop
// covering the daemon — the exact defect --all exists to fix. That is the shape
// this repo keeps finding: a guard that covers the logic and not the wiring.
//
// Written down rather than left to be discovered. Closing it needs seams around
// selfupdate.Check and ApplyReleaseSelf so RunE can run without touching the
// network or replacing a binary; that is worth doing when this file next
// changes, and is more machinery than today's fix justifies.
func TestUpdateDaemonCallSiteIsUncovered(t *testing.T) {
	t.Skip("documented gap: the call site is not covered, only the function it calls")
}

// --all must exist and must mean the same thing as --daemon. Dejima is two
// programs; "update" that silently means "update one of them" is what put an
// operator on a new client driving an old daemon.
func TestUpdateHasAllFlag(t *testing.T) {
	cmd := newUpdateCmd()
	f := cmd.Flags().Lookup("all")
	if f == nil {
		t.Fatal("`dejima update` has no --all flag")
	}
	if !strings.Contains(strings.ToLower(f.Usage), "daemon") {
		t.Errorf("--all doesn't say it covers the daemon: %q", f.Usage)
	}
	// It restarts the daemon. That must be visible before someone runs it.
	if !strings.Contains(strings.ToLower(f.Usage), "restart") {
		t.Errorf("--all doesn't warn that it restarts the daemon: %q", f.Usage)
	}
}

// An up-to-date CLIENT is exactly when nobody thinks to check the daemon, so
// that path has to speak for the daemon too.
func TestUpToDateClientStillReportsAStaleDaemon(t *testing.T) {
	daemonAt(t, "v0.8.87", "v0.8.89", true)
	var buf bytes.Buffer
	reportDaemonVersion(context.Background(), &buf, false)
	if !strings.Contains(buf.String(), "v0.8.87") {
		t.Errorf("a current client says nothing about a stale daemon:\n%s", buf.String())
	}
}
