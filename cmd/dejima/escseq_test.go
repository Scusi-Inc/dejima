package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeClock lets a test move time by hand, so the escape-sequence window is
// exercised without sleeping — a sleep-based test of a 50ms window is a flake
// waiting for a loaded CI runner.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func clockedModel(t *testing.T) (tuiModel, *fakeClock) {
	t.Helper()
	m := seededModel(t, island("alpha", "a1"))
	clk := &fakeClock{t: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
	m.nowFn = clk.now
	return m, clk
}

// press feeds one key through the real dispatch path, exactly as Update does.
func press(m tuiModel, key string) tuiModel {
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return out.(tuiModel)
}

func pressNamed(m tuiModel, k tea.KeyType) tuiModel {
	out, _ := m.handleKey(tea.KeyMsg{Type: k})
	return out.(tuiModel)
}

// THE REPORTED BUG. The operator's terminal delivered ESC [ A as three separate
// keypresses, so the up arrow opened the audit ledger. Nothing in the binding
// table was wrong; the transport was, and every bare uppercase binding was a
// live wire.
func TestSplitArrowSequenceDoesNotFireTheBindingOnItsTailByte(t *testing.T) {
	m, _ := clockedModel(t)

	m = pressNamed(m, tea.KeyEsc)
	m = press(m, "[")
	m = press(m, "A")

	if m.audit != nil {
		t.Error("up arrow, delivered as esc + [ + A, opened the audit ledger — the exact reported bug")
	}
}

// The same shape at the other end of the alphabet, and the sharper one: in the
// island creator, End is ESC [ F and `F` created the island with NO GitHub
// identity. An arrow key must not be able to make that decision.
func TestSplitEndSequenceCannotSkipTheGitHubIdentityGate(t *testing.T) {
	m, _ := clockedModel(t)
	m.creator = &creatorModel{step: stepGitHubGate}

	m = pressNamed(m, tea.KeyEsc)
	m = press(m, "[")
	m = press(m, "F")

	// NOTE WHAT THIS DOES NOT ASSERT. The leading esc still reaches the creator
	// and cancels it, so on a splitting terminal End closes the wizard. That is a
	// real remaining defect and it is recorded in escseq.go rather than hidden
	// here — fixing it means holding a bare esc back until the next byte, which
	// is a latency change for every working terminal. What this test pins is the
	// part that must never happen: the sequence's TAIL taking a decision.
	if m.creator == nil {
		return // esc cancelled the creator; the dangerous branch cannot have run
	}
	if m.creator.forceNoIdentity {
		t.Error("End, delivered as esc + [ + F, created the island with no GitHub identity")
	}
	if m.creator.creating {
		t.Error("End, delivered as esc + [ + F, started the create")
	}
}

// Every tail byte, through the real dispatch. A table rather than one case
// because the bug was reported for exactly one arrow — the one whose letter
// happened to be bound — and the others were invisible only because B, C and D
// did nothing on that screen.
func TestEveryEscapeSequenceTailIsSwallowed(t *testing.T) {
	for _, tail := range []string{"A", "B", "C", "D", "H", "F", "P", "Q", "R", "S", "Z", "~"} {
		m, _ := clockedModel(t)
		m = pressNamed(m, tea.KeyEsc)
		m = press(m, "[")
		before := m
		m = press(m, tail)
		if m.audit != nil || m.grants != nil || m.scope != nil || m.menu != nil || m.team != nil {
			t.Errorf("tail %q opened an overlay when it should have been swallowed", tail)
		}
		if m.selected != before.selected {
			t.Errorf("tail %q moved the selection", tail)
		}
	}
}

// A modified arrow is ESC [ 1 ; 5 A. The parameter bytes have to stay inside the
// sequence too, or a digit lands as a command and the tail lands after it.
func TestModifiedArrowParametersStayInsideTheSequence(t *testing.T) {
	m, _ := clockedModel(t)
	m = pressNamed(m, tea.KeyEsc)
	m = press(m, "[")
	m = press(m, "1")
	m = press(m, ";")
	m = press(m, "5")
	m = press(m, "A")
	if m.audit != nil {
		t.Error("a modified up arrow opened the audit ledger")
	}
}

// THE OTHER DIRECTION, and the one that decides whether this guard is worth
// having: a person pressing Esc and then a letter must not lose the letter.
// Without the time window this would swallow it, and the guard would be a new
// bug for every terminal that never had the old one.
func TestAKeyTypedAfterAPauseIsNotSwallowed(t *testing.T) {
	m, clk := clockedModel(t)

	m = pressNamed(m, tea.KeyEsc)
	clk.advance(escSequenceWindow + time.Millisecond)
	m = press(m, "A")

	if m.audit == nil {
		t.Error("esc, then A a moment later, must open the audit ledger — that is a person, not a sequence")
	}
}

// And immediately after an esc, a key that is NOT part of any sequence still
// works: the guard reacts to sequence bytes, not to everything following an esc.
func TestANonSequenceKeyAfterEscStillActs(t *testing.T) {
	m, _ := clockedModel(t)
	m = pressNamed(m, tea.KeyEsc)
	m = press(m, "T") // grants — not a tail byte
	if m.grants == nil {
		t.Error("T right after esc must still open the grants view")
	}
}

// Esc itself keeps working, on the same keypress, with no delay. Holding it back
// to see what follows would trade a broken terminal's bug for a latency change
// on every working one.
func TestEscStillActsImmediately(t *testing.T) {
	m, _ := clockedModel(t)
	m.imageBuiltPending = "image built — now upgrade an island"

	m = pressNamed(m, tea.KeyEsc)

	if m.imageBuiltPending != "" {
		t.Error("esc must dismiss the banner on the keypress itself, not after a timeout")
	}
	if m.escSeqState != escSeqSawEsc {
		t.Error("esc must also arm the sequence guard — it is both a key and a possible prefix")
	}
}

// ctrl+[ is the byte-identical alternative with no ambiguity after it, which is
// what makes it the remedy for an Esc that may never arrive intact. It must
// close a pane exactly as esc does.
func TestCtrlBracketClosesAPaneLikeEsc(t *testing.T) {
	m, _ := clockedModel(t)
	m.audit = &auditView{}
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlOpenBracket})
	if out.(tuiModel).audit != nil {
		t.Error("ctrl+[ must close the audit pane, like esc")
	}
}
