package main

import "os"

// clientTerminal reports this terminal's identity for the session handshake —
// the TERM/COLORTERM the daemon forwards into the island (see bridge.TermEnv),
// and therefore what image/tmux.conf's capability gate matches on.
func clientTerminal() (term, colorterm string) {
	return resolveTerminal(os.Getenv("TERM"), os.Getenv("COLORTERM"), os.Getenv("WT_SESSION"))
}

// resolveTerminal decides what to claim, given the raw environment.
//
// TERM is a Unix convention and native Windows does not set it. Left alone, a
// Windows client sends nothing, the daemon omits `docker exec -e TERM=`, and the
// island inherits docker's own default of a bare `xterm` — terminfo's 8-color
// entry. Since image/tmux.conf gates RGB/extkeys/sync on `*-256color`, that
// excludes every Windows user from all three no matter how capable their
// terminal actually is. The gate would be doing its job and still be wrong.
//
// So infer, but only where the inference is defensible. WT_SESSION is set by
// Windows Terminal on every session and is its documented detection signal.
// Windows Terminal does truecolor, and with prepareConsoleOutput setting
// DISABLE_NEWLINE_AUTO_RETURN it has the deferred end-of-line wrap those
// features assume — so claiming xterm-256color for it is a statement about
// capabilities we have actually established, not a hopeful guess.
//
// Everything else is left exactly as found. Legacy conhost in particular stays
// empty and stays gated out: the entire reason the gate exists is that ConPTY
// parses these sequences less forgivingly than a kernel pty, and guessing on its
// behalf is what produced the left-column smearing in the first place.
//
// An explicitly set TERM always wins, on every platform. Off Windows the
// environment is already authoritative (every emulator sets TERM), so this is a
// pure passthrough; under WSL inside Windows Terminal both signals are present
// and the real TERM is the better answer. Only the genuinely-empty case is
// filled in, which is why this needs no build tags.
func resolveTerminal(term, colorterm, wtSession string) (string, string) {
	if wtSession == "" {
		return term, colorterm
	}
	if term == "" {
		term = "xterm-256color"
	}
	if colorterm == "" {
		colorterm = "truecolor"
	}
	return term, colorterm
}
