package main

import (
	"os"
	"strings"
)

// terminalReport is what this client will claim about its terminal, and what the
// island will do with that claim.
type terminalReport struct {
	Term      string // what gets sent as TERM (possibly inferred)
	ColorTerm string // what gets sent as COLORTERM
	Inferred  bool   // the environment set no TERM and we supplied one
	Rich      bool   // Term matches image/tmux.conf's `*-256color` gate
}

// describeTerminal is the pure decision behind the doctor's Terminal section,
// separated from os.Getenv so it can be tested on any platform — the case that
// matters most (Windows sets no TERM) is unreachable on the machines that run
// the tests.
func describeTerminal(envTerm, envColorTerm, wtSession string) terminalReport {
	t, c := resolveTerminal(envTerm, envColorTerm, wtSession)
	return terminalReport{
		Term:      t,
		ColorTerm: c,
		Inferred:  strings.TrimSpace(envTerm) == "" && t != "",
		// The island gates RGB/extkeys/sync on `*-256color` (image/tmux.conf), and
		// bridge.canonicalTERM folds anything it can't resolve to xterm-256color —
		// so matching that suffix is exactly "the session will render in full
		// colour". A bare `xterm` deliberately does not.
		Rich: strings.HasSuffix(t, "-256color"),
	}
}

// checkTerminal reports what the island will be told about this terminal.
//
// This is the only place that answers "is my session actually going to render in
// truecolour, and why" without attaching to an island and typing tmux commands
// at it. It matters because the answer is invisible otherwise: the client sends
// TERM/COLORTERM in its first resize envelope, the daemon forwards them via
// `docker exec -e`, and the island's tmux gates its capabilities on the result —
// three hops, none of which the user can see.
func checkTerminal(r *doctorReport) {
	rep := describeTerminal(os.Getenv("TERM"), os.Getenv("COLORTERM"), os.Getenv("WT_SESSION"))

	switch {
	case rep.Term == "":
		// Native Windows outside Windows Terminal (legacy conhost) lands here.
		r.add("Terminal", "TERM", "WARN",
			"unset — the island falls back to a bare `xterm` (8 colours, no truecolour)",
			"use Windows Terminal, or set TERM=xterm-256color if this terminal supports it")
	case rep.Inferred:
		r.add("Terminal", "TERM", "OK",
			rep.Term+" (inferred: WT_SESSION → Windows Terminal; this OS sets no TERM)", "")
	default:
		r.add("Terminal", "TERM", "OK", rep.Term+" (from $TERM)", "")
	}

	if rep.ColorTerm != "" {
		r.add("Terminal", "COLORTERM", "INFO", rep.ColorTerm, "")
	}

	// The bottom line, stated plainly: this is the thing people actually want to
	// know, and deriving it from the TERM string is exactly the step that sent
	// people to `tmux list-clients` before.
	if rep.Rich {
		r.add("Terminal", "island rendering", "OK",
			"full colour — sessions get RGB, synchronised output and extended keys", "")
	} else {
		r.add("Terminal", "island rendering", "INFO",
			"reduced — no truecolour, synchronised output or extended keys for "+rep.Term,
			"this is deliberate: advertising them to a terminal that can't parse them smears output (see image/tmux.conf)")
	}
}

// checkTerminalClipboard reports a terminal that cannot carry the clipboard
// out of a child shell.
//
// Terminal.app does not implement OSC 52, so text copied inside an island
// session never reaches the Mac's clipboard, and an agent's auth URL cannot be
// copied at all. Dejima cannot fix that — it is the emulator's missing
// feature, not ours.
//
// What Dejima CAN fix is that the fact evaporated. macTermNudge() says this
// once, at first launch, and then writes a marker so it never says it again —
// a one-time notice about a PERMANENT property of the machine. An operator
// read it (or didn't) on day one, hit the clipboard wall days later, and had
// nothing left to connect the two; he reported it as a bug. Doctor is where
// someone goes when something is wrong, it is idempotent, and it never
// mentioned this. Now it does.
//
// INFO, not WARN: the setup is working as well as that terminal permits, and
// doctor must not exit non-zero over a choice of terminal.
func checkTerminalClipboard(r *doctorReport) {
	if currentTerminal() != terminalAppleTerminal {
		return
	}
	r.add("Terminal", "clipboard", "INFO",
		"Terminal.app has no OSC 52 — text copied inside an island session will not reach "+
			"the system clipboard, and an agent's auth URL cannot be copied",
		"use iTerm2, WezTerm or Ghostty for clipboard and tabs; Terminal.app also opens every "+
			"agent in a separate window")
}
