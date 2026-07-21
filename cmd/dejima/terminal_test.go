package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/clientcfg"
)

// classifyTerminal must map each terminal's env signature to the right kind,
// including kitty's TERM/KITTY_WINDOW_ID signals (TERM_PROGRAM often unset) and
// an unknown fallthrough.
func TestClassifyTerminal(t *testing.T) {
	cases := []struct {
		name          string
		termProgram   string
		term          string
		kittyWindowID string
		want          terminalKind
	}{
		{"iterm2", "iTerm.app", "xterm-256color", "", terminalITerm2},
		{"apple_terminal", "Apple_Terminal", "xterm-256color", "", terminalAppleTerminal},
		{"wezterm", "WezTerm", "xterm-256color", "", terminalWezTerm},
		{"ghostty", "ghostty", "xterm-ghostty", "", terminalGhostty},
		{"warp", "WarpTerminal", "xterm-256color", "", terminalWarp},
		{"kitty_via_term", "", "xterm-kitty", "", terminalKitty},
		{"kitty_via_windowid", "", "xterm-256color", "3", terminalKitty},
		{"kitty_both_signals", "", "xterm-kitty", "3", terminalKitty},
		{"unknown_empty", "", "", "", terminalUnknown},
		{"unknown_plain_xterm", "", "xterm-256color", "", terminalUnknown},
		{"unknown_other_program", "Hyper", "xterm-256color", "", terminalUnknown},
		// TERM_PROGRAM wins over a kitty-looking TERM (defensive; shouldn't co-occur).
		{"program_beats_term", "iTerm.app", "xterm-kitty", "", terminalITerm2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTerminal(tc.termProgram, tc.term, tc.kittyWindowID); got != tc.want {
				t.Errorf("classifyTerminal(%q,%q,%q) = %v, want %v",
					tc.termProgram, tc.term, tc.kittyWindowID, got, tc.want)
			}
		})
	}
}

func TestParseTerminalKind(t *testing.T) {
	overrides := map[string]terminalKind{
		"ghostty":  terminalGhostty,
		"Ghostty":  terminalGhostty,
		"iterm":    terminalITerm2,
		"iterm2":   terminalITerm2,
		"terminal": terminalAppleTerminal,
		"wezterm":  terminalWezTerm,
		"kitty":    terminalKitty,
		"warp":     terminalWarp,
	}
	for s, want := range overrides {
		if k, ok := parseTerminalKind(s); !ok || k != want {
			t.Errorf("parseTerminalKind(%q) = (%v,%v), want (%v,true)", s, k, ok, want)
		}
	}
	// "" / auto / garbage = not an override (auto-detect).
	for _, s := range []string{"", "auto", "auto-detect", "nonsense"} {
		if _, ok := parseTerminalKind(s); ok {
			t.Errorf("parseTerminalKind(%q) should not be an override", s)
		}
	}
}

// TestResolveTerminalKind pins the precedence: DEJIMA_TERMINAL env > the
// clientcfg setting > auto-detect from TERM_PROGRAM.
func TestResolveTerminalKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Env override wins over everything (even a conflicting TERM_PROGRAM).
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("DEJIMA_TERMINAL", "ghostty")
	if got := resolveTerminalKind(); got != terminalGhostty {
		t.Errorf("env override = %v, want ghostty", got)
	}

	// No env → the saved setting wins over auto-detect.
	t.Setenv("DEJIMA_TERMINAL", "")
	if err := clientcfg.Save(clientcfg.Config{Terminal: "iterm"}); err != nil {
		t.Fatal(err)
	}
	if got := resolveTerminalKind(); got != terminalITerm2 {
		t.Errorf("clientcfg override = %v, want iterm2", got)
	}

	// No env, no setting → auto-detect from TERM_PROGRAM.
	if err := clientcfg.Save(clientcfg.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM_PROGRAM", "ghostty")
	if got := resolveTerminalKind(); got != terminalGhostty {
		t.Errorf("auto-detect = %v, want ghostty", got)
	}
}

func TestSettingsListsDefaultTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if out := (tuiModel{settings: &settingsModel{page: settingsTop}}).renderSettings(); !strings.Contains(out, "Default terminal") {
		t.Errorf("settings top page should list a Default terminal entry\n%s", out)
	}
	if out := (tuiModel{settings: &settingsModel{page: settingsTerminal}}).renderSettings(); !strings.Contains(out, "Ghostty") || !strings.Contains(out, "default terminal") {
		t.Errorf("the terminal sub-page should render the choices\n%s", out)
	}
}
