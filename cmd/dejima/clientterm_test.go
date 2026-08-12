package main

import (
	"strings"
	"testing"
)

// resolveTerminal decides what a client claims about itself, which the daemon
// forwards into the island and image/tmux.conf then gates RGB/extkeys/sync on.
// Both directions matter: claiming too little strands a capable terminal in
// 8-color mode, claiming too much is what smeared ConPTY output to begin with.
func TestResolveTerminal(t *testing.T) {
	cases := []struct {
		name                    string
		term, colorterm, wt     string
		wantTerm, wantColorterm string
	}{
		// Off Windows the environment is authoritative — pure passthrough.
		{"unix terminal passes through", "xterm-256color", "truecolor", "", "xterm-256color", "truecolor"},
		{"unix without colorterm", "screen-256color", "", "", "screen-256color", ""},

		// Native Windows sets no TERM. Without a signal we must stay silent: the
		// island then inherits docker's bare `xterm` and is correctly excluded,
		// which is the right answer for legacy conhost.
		{"windows conhost stays empty", "", "", "", "", ""},

		// Windows Terminal identifies itself via WT_SESSION, does truecolor, and
		// (with prepareConsoleOutput) has the deferred EOL wrap these features
		// assume — enough to claim 256-color honestly.
		{"windows terminal is inferred", "", "", "abc-123", "xterm-256color", "truecolor"},

		// An explicit TERM always wins. Under WSL inside Windows Terminal both
		// signals are present and the real TERM is the better answer.
		{"explicit TERM beats the guess", "xterm-kitty", "", "abc-123", "xterm-kitty", "truecolor"},
		{"wsl inside windows terminal", "screen-256color", "truecolor", "abc-123", "screen-256color", "truecolor"},
		{"explicit COLORTERM is preserved", "", "24bit", "abc-123", "xterm-256color", "24bit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotTerm, gotColor := resolveTerminal(c.term, c.colorterm, c.wt)
			if gotTerm != c.wantTerm || gotColor != c.wantColorterm {
				t.Errorf("resolveTerminal(%q, %q, %q) = (%q, %q), want (%q, %q)",
					c.term, c.colorterm, c.wt, gotTerm, gotColor, c.wantTerm, c.wantColorterm)
			}
		})
	}
}

// The value we invent for Windows Terminal is load-bearing in two places it
// cannot see: it must match image/tmux.conf's `*-256color` gate (or the whole
// exercise buys nothing), and it must resolve in the island's terminfo, which
// ships ncurses-base only (or tmux dies on attach with "missing or unsuitable
// terminal"). bridge.canonicalTERM would fold an unknown name to xterm-256color
// anyway, but arriving already-correct means no silent rewrite in between.
func TestInferredWindowsTermMatchesTheGate(t *testing.T) {
	got, _ := resolveTerminal("", "", "some-wt-session")
	if !strings.HasSuffix(got, "-256color") {
		t.Errorf("inferred TERM %q does not match image/tmux.conf's `*-256color` gate", got)
	}
	if got != "xterm-256color" {
		t.Errorf("inferred TERM = %q; want xterm-256color, the entry ncurses-base guarantees", got)
	}
}
