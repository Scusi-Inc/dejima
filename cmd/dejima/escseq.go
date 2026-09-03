package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Reassembling — or at least refusing to act on — escape sequences that arrive
// as separate keypresses.
//
// THE FAILURE THIS EXISTS FOR. An operator on Windows reported that pressing UP
// ARROW opened the audit ledger, Enter needed two presses, and Esc did nothing.
// Up is ESC [ A; their terminal delivered those three bytes as three keypresses,
// so the trailing `A` landed on the audit binding. Down/Right/Left appeared to do
// nothing because B, C and D happened to be unbound on that screen — and Right
// and End DID fire in the island creator, where `C` opened a GitHub window and
// `F` created the island with no identity at all.
//
// WHY THIS RATHER THAN REBINDING. Rebinding the letters off the sequence bytes
// fixes today's collisions and nothing else: the next uppercase binding is one
// commit away, and there is nothing about A/C/F/H/P/R/S that makes them look
// dangerous to the person adding one. This makes the whole class inert, and the
// audit test (keybinding_audit_test.go) then keeps the letter list honest rather
// than being the only defence. Muscle memory is preserved either way, which
// rebinding cannot claim.
//
// WHAT IT DOES NOT DO, STATED PLAINLY BECAUSE IT IS A REMAINING DEFECT AND NOT A
// DESIGN CHOICE I AM HAPPY WITH:
//
//  1. It does not reconstruct the arrow key. On a splitting terminal the arrows
//     become no-ops rather than wrong actions — the difference between "the
//     arrows don't work" and "the up arrow erased something", but not a fix.
//  2. THE LEADING ESC STILL ACTS. Pressing End inside the island creator cancels
//     it, because the esc arrives first and esc means cancel. Suppressing that
//     means holding a bare esc back until the next byte or a timer, which is a
//     latency change on every working terminal to fix one broken one. If we do
//     it, the version worth building is ADAPTIVE: hold esc only after this
//     process has actually seen a split sequence, so terminals that never had
//     the bug never pay for it.
//  3. IT ASSUMES THE ESC ARRIVES AT ALL. The operator reported "Esc does
//     nothing", which has two readings: esc is delivered and something ignores
//     it, or esc is swallowed as a sequence start and never surfaces. If it is
//     the second, this guard never arms, because there is no esc to arm it —
//     and `[` would fire the reorder binding directly. Nobody has measured which
//     it is. DEJIMA_KEYLOG (see keylog.go) is there to settle it with data
//     rather than another round of reasoning.

// escSequenceWindow is how long after an ESC a byte is still assumed to belong
// to an escape sequence rather than to a person.
//
// A terminal that splits a sequence still emits its bytes back to back; the
// delay is a scheduling artifact, not a pause. A human pressing Esc and then a
// letter cannot do it in 50ms. So the window separates the two cases on the one
// dimension where they are nowhere near each other, and being wrong costs a
// single keystroke either way.
const escSequenceWindow = 50 * time.Millisecond

// escSeqIntro are the bytes that FOLLOW an ESC to open a sequence.
var escSeqIntro = map[string]bool{"[": true, "O": true}

// escSeqTail are the bytes that END the sequences a keyboard sends. Kept beside
// csiDangerous in the audit test, which asserts the two agree — one of them
// gaining a byte the other lacks is how this guard would develop a hole.
var escSeqTail = map[string]bool{
	"A": true, "B": true, "C": true, "D": true, // arrows
	"H": true, "F": true, // home / end
	"P": true, "Q": true, "R": true, "S": true, // F1-F4 via SS3
	"Z": true, // shift-tab
	"~": true, // the numbered forms
}

// swallowEscapeSequenceByte decides whether this keypress is a fragment of a
// split escape sequence rather than a command, and advances the little state
// machine that tracks one.
//
// Returns the updated model and true when the byte should be DROPPED.
//
// The state is deliberately three values and not a buffer: esc seen, intro seen,
// idle. Anything that does not continue a sequence resets it, so a stray ESC
// costs at most the two keystrokes after it, and only if they look like sequence
// bytes within the window.
func (m tuiModel) swallowEscapeSequenceByte(msg tea.KeyMsg) (tuiModel, bool) {
	// A zero-valued model (several tests build one directly) has no clock. Default
	// rather than panic: a guard that crashes the dashboard because someone
	// constructed a model the short way is worse than the bug it prevents.
	if m.nowFn == nil {
		m.nowFn = time.Now
	}
	now := m.nowFn()
	s := msg.String()

	// An ESC always (re)starts a sequence AND is still delivered: a real Esc must
	// keep working on every normal terminal, where it is the only thing that
	// arrives. Holding it back to see what follows is what would make this a
	// latency change for everyone rather than a guard for the broken case.
	if s == "esc" {
		m.escSeqAt, m.escSeqState = now, escSeqSawEsc
		return m, false
	}

	// Outside the window, whatever state we were in is stale — a person, not a
	// sequence.
	if m.escSeqState == escSeqIdle || now.Sub(m.escSeqAt) > escSequenceWindow {
		m.escSeqState = escSeqIdle
		return m, false
	}

	switch {
	case m.escSeqState == escSeqSawEsc && escSeqIntro[s]:
		// ESC [ — the sequence is opening. Drop the intro byte, keep waiting.
		m.escSeqState, m.escSeqAt = escSeqSawIntro, now
		return m, true
	case m.escSeqState == escSeqSawIntro && escSeqTail[s]:
		// ESC [ A — the whole sequence has now been consumed.
		m.escSeqState = escSeqIdle
		return m, true
	case m.escSeqState == escSeqSawIntro:
		// A parameter byte (ESC [ 1 ; 5 A for a modified arrow). Stay in the
		// sequence rather than treating a digit as a command.
		if len(s) == 1 && (s[0] >= '0' && s[0] <= '9' || s[0] == ';') {
			m.escSeqAt = now
			return m, true
		}
	case m.escSeqState == escSeqSawEsc && escSeqTail[s]:
		// ESC A, with no intro byte: the SS3-style two-byte forms, and the shape
		// left over when an intro byte is lost rather than delayed.
		m.escSeqState = escSeqIdle
		return m, true
	}

	m.escSeqState = escSeqIdle
	return m, false
}

// escSeqState values.
const (
	escSeqIdle = iota
	escSeqSawEsc
	escSeqSawIntro
)
