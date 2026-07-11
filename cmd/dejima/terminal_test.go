package main

import "testing"

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
