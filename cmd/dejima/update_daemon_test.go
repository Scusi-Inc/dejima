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
// A mutation proved this: removing the call passed the whole suite. That is the
// exact shape this repo keeps finding — a guard that covers the logic and not
// the wiring — so it is written down instead of being discovered later.
func TestUpdateDaemonCallSiteIsUncovered(t *testing.T) {
	t.Skip("documented gap: the call site is not covered, only the function it calls")
}
