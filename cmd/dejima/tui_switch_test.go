package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aoos/dejima/internal/clientcfg"
)

// The pane viewports must never exceed innerH lines — that bound is what keeps
// the header from being pushed off the top of the screen.
func TestPaneWindows(t *testing.T) {
	content := strings.Join([]string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}, "\n") // 10 lines

	// Fits → unchanged, no scroll.
	if got, max := scrollWindow(content, 20, 0); got != content || max != 0 {
		t.Errorf("fit: unexpected window/max (%d)", max)
	}

	// Overflows → exactly innerH lines (visN content + 1 hint), correct maxOff.
	got, max := scrollWindow(content, 5, 0)
	if n := strings.Count(got, "\n") + 1; n != 5 {
		t.Errorf("scrollWindow height = %d, want 5", n)
	}
	if max != 6 { // len(10) - visN(4)
		t.Errorf("maxOff = %d, want 6", max)
	}
	// Offset past the end clamps to the last window.
	if got, _ := scrollWindow(content, 5, 999); !strings.HasPrefix(strings.Split(got, "\n")[0], "6") {
		t.Errorf("clamped window should start at line 6, got %q", strings.Split(got, "\n")[0])
	}

	// followWindow keeps the selected line visible, still bounded to innerH.
	fw := followWindow(content, 5, 8)
	if n := strings.Count(fw, "\n") + 1; n != 5 {
		t.Errorf("followWindow height = %d, want 5", n)
	}
	if !strings.Contains(fw, "8") {
		t.Errorf("selected line 8 not in window: %q", fw)
	}
}

// A bare host (no :port) must get the default daemon port — a port-less host
// otherwise dials :80 and is refused (the connection bug operators hit).
func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"100.77.85.107":      "100.77.85.107:7273",
		"100.77.85.107:7273": "100.77.85.107:7273",
		"minion":             "minion:7273",
		"minion:7273":        "minion:7273",
		"  100.77.85.107  ":  "100.77.85.107:7273",
		"minion:9999":        "minion:9999", // a non-default port is preserved
		"":                   "",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The header announcement bar shows only when there's something to broadcast,
// and an available update is its first source.
func TestAnnouncement(t *testing.T) {
	if _, _, _, ok := (tuiModel{}).announcement(); ok {
		t.Fatal("expected no announcement when up to date")
	}
	m := tuiModel{clientUpdate: true, latestRelease: "v9.9.9"}
	full, short, _, ok := m.announcement()
	if !ok {
		t.Fatal("expected an announcement when a client update is available")
	}
	if !strings.Contains(full, "update available") || !strings.Contains(full, "[U] update") {
		t.Fatalf("full bar missing content: %q", full)
	}
	if !strings.Contains(short, "[U]") {
		t.Fatalf("compact chip missing action: %q", short)
	}
}

// TestAnnouncementLifecycle locks in the update-bar states and their precedence:
// red failure > orange restart-pending > green applied > amber available. The
// styles differ per state so each reads distinctly.
func TestAnnouncementLifecycle(t *testing.T) {
	// lipgloss.Style isn't comparable (it holds funcs), so identify a style by
	// what it renders.
	sameStyle := func(a, b lipgloss.Style) bool { return a.Render("x") == b.Render("x") }
	full := func(m tuiModel) (string, lipgloss.Style) {
		f, _, st, ok := m.announcement()
		if !ok {
			t.Helper()
			t.Fatal("expected an announcement")
		}
		return f, st
	}

	applied, appliedStyle := full(tuiModel{updateApplied: "daemon updated to v9 — restarting"})
	if !strings.Contains(applied, "✓") || !sameStyle(appliedStyle, styleSuccessBroadcast) {
		t.Errorf("applied should be a green ✓ banner: %q", applied)
	}
	restart, restartStyle := full(tuiModel{restartPending: "client updated to v9 — restart dejima to apply"})
	if !strings.Contains(restart, "restart") || !sameStyle(restartStyle, styleWarnBroadcast) {
		t.Errorf("restart-pending should be an orange banner: %q", restart)
	}
	failed, failStyle := full(tuiModel{updateError: "boom"})
	if !strings.Contains(failed, "retry") || !sameStyle(failStyle, styleErrorBroadcast) {
		t.Errorf("failure should be a red retry banner: %q", failed)
	}

	// Precedence: a failure outranks a pending restart, which outranks a fading
	// success, which outranks the plain "available" prompt.
	_, st := full(tuiModel{updateError: "boom", restartPending: "x", updateApplied: "y", clientUpdate: true})
	if !sameStyle(st, styleErrorBroadcast) {
		t.Error("failure must outrank every other update banner")
	}
	_, st = full(tuiModel{restartPending: "x", updateApplied: "y", clientUpdate: true})
	if !sameStyle(st, styleWarnBroadcast) {
		t.Error("restart-pending must outrank applied + available")
	}
	_, st = full(tuiModel{updateApplied: "y", clientUpdate: true})
	if !sameStyle(st, styleSuccessBroadcast) {
		t.Error("applied must outrank the available prompt")
	}
}

// TestUpdateNoticeFade: the green banner clears only when the fade tick matches
// the token that armed it — a newer banner set in between must survive.
func TestUpdateNoticeFade(t *testing.T) {
	var m tuiModel
	m.showUpdateApplied("first")
	stale := m.applyToken
	m.showUpdateApplied("second") // a newer success takes the slot + its own token

	out, _ := m.Update(updateNoticeFadedMsg{token: stale})
	if got := out.(tuiModel).updateApplied; got != "second" {
		t.Errorf("stale fade wiped a newer banner: updateApplied=%q, want %q", got, "second")
	}
	out, _ = out.(tuiModel).Update(updateNoticeFadedMsg{token: out.(tuiModel).applyToken})
	if got := out.(tuiModel).updateApplied; got != "" {
		t.Errorf("matching fade should clear the banner, got %q", got)
	}
}

// resolveTarget is the single source of truth for the connection target. It must
// honor DEJIMA_HOST first (override + in-island path), then the saved active
// profile (so a remote target survives restarts), then the local socket.
func TestResolveTargetPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // redirect ~/.dejima for clientcfg
	if err := clientcfg.Save(clientcfg.Config{
		Profiles:      []clientcfg.Profile{{Name: "minion", Host: "100.77.85.107:7273"}},
		ActiveProfile: "minion",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("env wins over saved profile", func(t *testing.T) {
		t.Setenv("DEJIMA_HOST", "host.docker.internal:7274")
		host, _, source := resolveTarget()
		if host != "host.docker.internal:7274" || source != "env" {
			t.Fatalf("env should win, got (%q, source=%q)", host, source)
		}
	})

	t.Run("saved active profile when env unset", func(t *testing.T) {
		t.Setenv("DEJIMA_HOST", "")
		host, label, source := resolveTarget()
		if host != "100.77.85.107:7273" || label != "minion" || source != "profile" {
			t.Fatalf("want (100.77.85.107:7273, minion, profile), got (%q, %q, %q)", host, label, source)
		}
	})
}

// A NUL delivered as a single rune is exactly what wedged a saved host into
// `http://\x00minion/...`; printableInput must drop it while keeping ordinary
// characters and spaces.
func TestPrintableInput(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"letter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}}, "m"},
		{"digit", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}}, "7"},
		{"colon", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}, ":"},
		{"space", tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, " "},
		{"nul", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}}, ""},
		{"del", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0x7f}}, ""},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, ""},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, ""},
		{"paste", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("minion")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := printableInput(tc.msg); got != tc.want {
				t.Fatalf("printableInput(%v) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// clientForHost is the choke point: a host carrying a control character must be
// rejected with a clear error rather than producing an unparseable request URL.
func TestClientForHostRejectsControlChars(t *testing.T) {
	if _, err := clientForHost("\x00minion:7273"); err == nil {
		t.Fatal("expected error for host with NUL, got nil")
	} else if !strings.Contains(err.Error(), "control character") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
}
