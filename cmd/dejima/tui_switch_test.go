package main

import (
	"errors"
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
		// A WSL distro is a name, not an address. Appending :7273 to it would
		// produce a target the wsl:// dialer can't resolve.
		"wsl://dejima":  "wsl://dejima",
		"wsl://Ubuntu":  "wsl://Ubuntu",
		"  wsl://dev  ": "wsl://dev",
		"wsl://":        "wsl://dejima", // shorthand fills in the default distro
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

// TestImageBuildAnnouncement locks in the image-build feedback loop. Both halves
// regressed silently before: the in-flight state was a lone footer glyph that a
// stale error hid outright, and a SUCCESSFUL build rendered nothing anywhere — so
// a finished build looked identical to one that never started.
func TestImageBuildAnnouncement(t *testing.T) {
	sameStyle := func(a, b lipgloss.Style) bool { return a.Render("x") == b.Render("x") }

	// In flight: an announcement, not just a footer glyph.
	full, _, st, ok := tuiModel{building: true}.announcement()
	if !ok {
		t.Fatal("a build in flight must broadcast something")
	}
	if !strings.Contains(full, "building the island image") || !sameStyle(st, styleWarnBroadcast) {
		t.Errorf("in-flight build should be an orange progress banner: %q", full)
	}

	// Starting a build clears a stale lastError, which renderFooterLeft would
	// otherwise early-return on — swallowing the footer's ⏳ for the whole build.
	out, _ := tuiModel{lastError: "some earlier op blew up"}.
		runConfirmed(confirmPrompt{verb: "build-image", answer: "y"})
	started := out.(tuiModel)
	if !started.building {
		t.Fatal("confirming build-image must set building")
	}
	if started.lastError != "" {
		t.Errorf("starting a build must clear a stale error, got %q", started.lastError)
	}

	// Success: banner names the SECOND step, since building alone moves no island.
	out, _ = started.Update(imageBuildDoneMsg{})
	done := out.(tuiModel)
	if done.building {
		t.Error("a finished build must clear building")
	}
	// Asserted against the label the menu ACTUALLY carries, not a copy of it: the
	// banner tells the operator to go find a named item, so a rename that doesn't
	// update both leaves an instruction pointing at something that isn't there.
	if want := upgradeMenuLabel(t); !strings.Contains(done.imageBuiltPending, want) {
		t.Errorf("success banner must name the upgrade item verbatim (%q), got %q", want, done.imageBuiltPending)
	}
	full, _, st, ok = done.announcement()
	if !ok || !strings.Contains(full, "[esc] dismiss") || !sameStyle(st, styleWarnBroadcast) {
		t.Errorf("built-image notice should be a sticky orange banner: %q", full)
	}

	// ...and [esc] dismisses it, like the other sticky banners.
	out, _ = done.handleKey(key("esc"))
	if got := out.(tuiModel).imageBuiltPending; got != "" {
		t.Errorf("esc must dismiss the built-image notice, got %q", got)
	}

	// A failed build reports the error and claims no success.
	out, _ = started.Update(imageBuildDoneMsg{err: errors.New("boom")})
	failed := out.(tuiModel)
	if !strings.Contains(failed.lastError, "boom") {
		t.Errorf("a failed build must surface the error, got %q", failed.lastError)
	}
	if failed.imageBuiltPending != "" {
		t.Errorf("a failed build must not claim the image was built, got %q", failed.imageBuiltPending)
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

// goldenInvite is the frozen vector a1 published with the invite backend (PR
// #219). Asserting against this exact string keeps the TUI join side in lockstep
// with the issuer/encoder; if either drifts, this fails.
const goldenInvite = "dejima-invite:eyJ2IjoxLCJob3N0IjoibWluaW9uLnRzLm5ldDo3Mjc0IiwidG9rZW4iOiJzZWtfYWJjMTIzIiwicm9sZSI6Im9wZXJhdG9yIiwiaXNsYW5kcyI6WyJ3ZWJhcHAiXSwibmFtZSI6Im1pbmlvbiIsImxhYmVsIjoiQW1hbmRhIn0"

// TestSwitcherJoinKeyEntry: J (and i) from the list opens the join-via-invite
// step; lowercase j stays list navigation.
func TestSwitcherJoinKeyEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"J", "i"} {
		out, _ := (tuiModel{switcher: &switcherModel{}}).switcherKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if got := out.(tuiModel).switcher.step; got != swJoin {
			t.Errorf("key %q should open swJoin, got step %d", key, got)
		}
	}
}

// TestSwitcherJoinPaste: the join field takes a multi-rune paste (a bracketed
// paste is one KeyRunes event) and backspace trims a rune.
func TestSwitcherJoinPaste(t *testing.T) {
	m := tuiModel{switcher: &switcherModel{step: swJoin}}
	out, _ := m.switcherJoinKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(goldenInvite)})
	m = out.(tuiModel)
	if m.switcher.blob != goldenInvite {
		t.Fatalf("paste should fill the blob, got %q", m.switcher.blob)
	}
	out, _ = m.switcherJoinKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m2 := out.(tuiModel); m2.switcher.blob != goldenInvite[:len(goldenInvite)-1] {
		t.Errorf("backspace should drop one rune, got %q", m2.switcher.blob)
	}
}

// TestSwitcherJoinDecodeError: a malformed paste keeps the user in the join step
// with the decoder's verbatim error — never a silent failure or a half-join.
func TestSwitcherJoinDecodeError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := tuiModel{switcher: &switcherModel{step: swJoin, blob: "not-an-invite"}}
	out, _ := m.switcherJoinSubmit()
	m = out.(tuiModel)
	if m.switcher == nil {
		t.Fatal("a decode error must not close the switcher")
	}
	if m.switcher.err == "" {
		t.Error("a decode error should be surfaced in the overlay")
	}
}

// TestSwitcherJoinGoldenBlob: pasting a1's frozen invite decodes, persists a
// profile carrying the host + token, makes it active, and connects (switcher
// closes). This is the teammate half of invite -> paste -> connect.
func TestSwitcherJoinGoldenBlob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_TOKEN", "") // so the persisted profile token is what resolves
	t.Setenv("DEJIMA_HOST", "")

	m := tuiModel{switcher: &switcherModel{step: swJoin, blob: goldenInvite}}
	out, cmd := m.switcherJoinSubmit()
	m = out.(tuiModel)
	if m.switcher != nil {
		t.Fatalf("a successful join should close the switcher (err=%q)", func() string {
			if m.switcher != nil {
				return m.switcher.err
			}
			return ""
		}())
	}
	if m.activeHost != "minion.ts.net:7274" {
		t.Errorf("activeHost = %q, want minion.ts.net:7274", m.activeHost)
	}
	if cmd == nil {
		t.Error("join should kick a fresh fetch")
	}
	// The profile + token must be persisted and active.
	cfg, err := clientcfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProfile != "minion" {
		t.Errorf("ActiveProfile = %q, want minion", cfg.ActiveProfile)
	}
	var found *clientcfg.Profile
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == "minion" {
			found = &cfg.Profiles[i]
		}
	}
	if found == nil {
		t.Fatal("joined profile not saved")
	}
	if found.Host != "minion.ts.net:7274" || found.Token != "sek_abc123" || found.Role != "operator" {
		t.Errorf("saved profile = %+v, want host=minion.ts.net:7274 token=sek_abc123 role=operator", *found)
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

// [d] used to delete a saved connection instantly. It now stages a confirmation,
// because the list's own Enter means "connect" — so a d-then-Enter reflex was one
// keystroke away from removing a profile the user meant to switch to. The rules
// under test: d stages (never deletes), only y commits, Enter is inert while the
// prompt is up, and the synthetic "local" row stays undeletable.
func TestSwitcherDeleteConfirmation(t *testing.T) {
	setup := func(t *testing.T) tuiModel {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		cfg, _ := clientcfg.Load()
		cfg.Profiles = []clientcfg.Profile{{Name: "minion", Host: "10.0.0.1:7273"}}
		if err := clientcfg.Save(cfg); err != nil {
			t.Fatalf("seed config: %v", err)
		}
		return tuiModel{switcher: &switcherModel{
			profiles: []clientcfg.Profile{{Name: "local"}, {Name: "minion", Host: "10.0.0.1:7273"}},
			cursor:   1,
		}}
	}
	press := func(m tuiModel, key string) tuiModel {
		var msg tea.KeyMsg
		switch key {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		out, _ := m.switcherKey(msg)
		return out.(tuiModel)
	}
	saved := func(t *testing.T) int {
		t.Helper()
		cfg, _ := clientcfg.Load()
		return len(cfg.Profiles)
	}

	t.Run("d stages the prompt without deleting", func(t *testing.T) {
		m := press(setup(t), "d")
		if m.switcher.step != swConfirmDelete {
			t.Fatalf("step = %d, want swConfirmDelete", m.switcher.step)
		}
		if n := saved(t); n != 1 {
			t.Fatalf("profile removed before confirming (%d left)", n)
		}
	})

	t.Run("enter does not confirm", func(t *testing.T) {
		m := press(press(setup(t), "d"), "enter")
		if m.switcher.step != swConfirmDelete {
			t.Errorf("Enter should leave the prompt up, got step %d", m.switcher.step)
		}
		if n := saved(t); n != 1 {
			t.Errorf("Enter deleted the profile (%d left)", n)
		}
	})

	for _, key := range []string{"n", "esc"} {
		t.Run(key+" cancels", func(t *testing.T) {
			m := press(press(setup(t), "d"), key)
			if m.switcher.step != swList {
				t.Errorf("%q should return to the list, got step %d", key, m.switcher.step)
			}
			if n := saved(t); n != 1 {
				t.Errorf("%q deleted the profile (%d left)", key, n)
			}
		})
	}

	t.Run("y commits and returns to the list", func(t *testing.T) {
		m := press(press(setup(t), "d"), "y")
		if m.switcher.step != swList {
			t.Errorf("after deleting, step = %d, want swList", m.switcher.step)
		}
		if n := saved(t); n != 0 {
			t.Errorf("y should have removed the profile, %d left", n)
		}
	})

	t.Run("local is undeletable", func(t *testing.T) {
		m := setup(t)
		m.switcher.cursor = 0
		if got := press(m, "d"); got.switcher.step != swList {
			t.Errorf("d on synthetic local should do nothing, got step %d", got.switcher.step)
		}
	})

	t.Run("prompt names the profile and warns when it is active", func(t *testing.T) {
		m := setup(t)
		m.activeHost = "10.0.0.1:7273" // connected through the row under the cursor
		m = press(m, "d")
		if !m.switcher.delActive {
			t.Error("delActive should be set when deleting the active connection")
		}
		v := m.switcher.view()
		for _, want := range []string{"minion", "10.0.0.1:7273", "active connection", "[y] delete"} {
			if !strings.Contains(v, want) {
				t.Errorf("confirm view missing %q:\n%s", want, v)
			}
		}
	})
}
