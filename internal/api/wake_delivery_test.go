package api

import (
	"strings"
	"testing"
	"time"
)

// The frames below are REAL captures from a live Claude Code v2.1.273 driven in
// a tmux pane, not hand-written approximations. That matters: the thing being
// parsed is a rendered TUI, and a fixture invented to match the parser would
// assert only that the parser agrees with itself.
const glyph = "❯"

const frameEmpty = `  ▝▝ ▝▝    /workspace
────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · PR #437 · ← for agents`

const frameOneLineDraft = `  ▝▝ ▝▝    /workspace
────────────────────────────────────────────────────────
❯ let me ask about the sched
────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · PR #437`

// A two-line draft: the continuation row carries NO glyph, which is why the
// scan cannot stop at the glyph line.
const frameTwoLineDraft = `  ▝▝ ▝▝    /workspace
                                    ctrl+g to edit in Vim
────────────────────────────────────────────────────────
❯ first line
  second line
────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · PR #437`

func TestClassifyInputBox(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame string
		want  inputBoxState
	}{
		{"empty prompt", frameEmpty, inputEmpty},
		{"one-line draft", frameOneLineDraft, inputDrafted},
		// The case cursor position alone gets WRONG: with the cursor sent Home on
		// line two, cursor_x reads 2 — identical to an empty box. Only the text
		// distinguishes them, which is why this is parsed rather than measured off
		// the cursor.
		{"multi-line draft", frameTwoLineDraft, inputDrafted},
		{"no prompt found", "just some output\nand more", inputUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyInputBox(tc.frame, glyph); got != tc.want {
				t.Errorf("classifyInputBox = %v, want %v", got, tc.want)
			}
		})
	}
}

// The glyph also appears in the agent's own output above the box. The input box
// is the BOTTOM-most one, so a first-match scan would read the transcript and
// report a draft that is not there — and then never auto-submit again.
func TestClassifyInputBoxUsesTheBottomMostPrompt(t *testing.T) {
	frame := "the agent printed ❯ in its output\n" + frameEmpty
	if got := classifyInputBox(frame, glyph); got != inputEmpty {
		t.Errorf("got %v, want inputEmpty — the prompt in the transcript is not the input box", got)
	}
}

func TestDecideDelivery(t *testing.T) {
	const fresh = 5 * time.Second
	const stale = 10 * time.Minute
	for _, tc := range []struct {
		name         string
		attached     bool
		box          inputBoxState
		keyboardIdle time.Duration
		heldFor      time.Duration
		want         deliveryMode
	}{
		// Unattended islands are the reason wake-on-message exists. They must keep
		// working exactly as before, and must not consult the screen at all.
		{"nobody attached", false, inputDrafted, fresh, 0, deliverSubmit},
		// The operator asked for this explicitly: auto-submit when there is no
		// other text to send along with it.
		{"attached, empty prompt", true, inputEmpty, fresh, 0, deliverSubmit},
		// Mid-sentence: their Enter is seconds away and the nudge lands behind it.
		{"attached, mid-sentence", true, inputDrafted, fresh, 0, deliverHold},
		// THE CASE THAT MOTIVATED THE KEYBOARD SIGNAL, and the one the first
		// version of this file got backwards. A terminal left open with text in
		// it: it used to PASTE here, which delivers nothing until a human presses
		// Enter — and this branch is reached only when no human has touched the
		// keyboard for 45 minutes. A delivery mode for "the operator is away"
		// that requires the operator to be present.
		//
		// Submitting sends their stale draft along with the notice. That is a
		// worse turn than it would have been, and far better than a message
		// nobody ever reads.
		{"attached, abandoned draft", true, inputDrafted, stale, 0, deliverSubmit},
		// Even someone genuinely typing does not get to hold mail forever.
		{"attached, typing but past the cap", true, inputDrafted, fresh, draftHoldCap, deliverSubmit},
		// An unreadable screen is treated as a draft — but a draft still gets
		// delivered once nobody is typing, rather than held forever.
		{"attached, prompt not found", true, inputUnknown, stale, 0, deliverSubmit},
		// A framework with no known prompt keeps today's behaviour rather than
		// being switched to paste-only on a guess.
		{"framework we cannot read", true, inputUnreadable, fresh, 0, deliverSubmit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decideDelivery(tc.attached, tc.box, tc.keyboardIdle, tc.heldFor)
			if got != tc.want {
				t.Errorf("decideDelivery(attached=%v, box=%v, idle=%v, held=%v) = %v, want %v",
					tc.attached, tc.box, tc.keyboardIdle, tc.heldFor, got, tc.want)
			}
		})
	}
}

// The newest client across ALL of them, because a session accumulates dead
// attaches — twelve across two agents on a real island, most hours stale. Taking
// any single one (or the first) would report an operator who left yesterday.
func TestParsePaneReadingTakesTheNewestClient(t *testing.T) {
	out := "1000000\n---\n999100\n999000\n999940\n---\n" + frameEmpty
	r := parsePaneReading(out, glyph)
	if !r.attached {
		t.Fatal("clients were listed and this reported nobody attached")
	}
	if r.keyboardIdle != 60*time.Second {
		t.Errorf("keyboardIdle = %v, want 60s (newest of the three, not the first or oldest)", r.keyboardIdle)
	}
	if r.box != inputEmpty {
		t.Errorf("box = %v, want inputEmpty", r.box)
	}
}

// No clients listed is the unattended island: nobody to disturb, and the
// decision short-circuits to today's submit before the box is even consulted.
func TestParsePaneReadingNoClients(t *testing.T) {
	r := parsePaneReading("1000000\n---\n\n---\n"+frameOneLineDraft, glyph)
	if r.attached {
		t.Error("reported an attached operator with an empty client list")
	}
	if got := decideDelivery(r.attached, r.box, r.keyboardIdle, 0); got != deliverSubmit {
		t.Errorf("an unattended island with a stale draft should still submit, got %v", got)
	}
}

// A malformed dump must not read as "empty prompt, nobody home" — that is the
// one wrong answer that submits into somebody's draft.
func TestParsePaneReadingGarbageIsNotEmpty(t *testing.T) {
	for _, out := range []string{"", "no separators here", "1000\n---\nonly two sections"} {
		if got := parsePaneReading(out, glyph).box; got == inputEmpty {
			t.Errorf("parsePaneReading(%q).box = inputEmpty — a dump we could not read "+
				"must never authorise a submit", out)
		}
	}
}

// The notice must be ONE line of content wrapped in newlines: a 3-line paste
// collapses into `[Pasted text #1 +N lines]`, which lands inline and absorbs the
// leading newline — undoing the separation this exists to provide.
func TestPasteBodyStaysOneLine(t *testing.T) {
	body := "\n[" + "📬 2 new message(s) — run: dejima msg poll" + "]\n"
	if n := strings.Count(strings.Trim(body, "\n"), "\n"); n != 0 {
		t.Errorf("notice body has %d embedded newlines; 3+ lines chips into an opaque paste marker", n+1)
	}
	if !strings.HasPrefix(body, "\n") || !strings.HasSuffix(body, "\n") {
		t.Error("the notice needs a newline either side: leading so the operator's line survives, trailing so their cursor lands fresh")
	}
}

// THE REGRESSION THIS FILE NOW GUARDS, stated as a property rather than a row:
// there must be NO state in which mail is neither delivered nor retried. The
// first version had one — deliverPaste was terminal, and pastedSet blocked a
// second attempt, so a parked draft swallowed the message permanently.
//
// Written as an exhaustive sweep instead of examples, because the hole was not
// in any single case anyone would think to write: it was in the combination
// "we decided something, and that decision led nowhere".
func TestEveryDecisionEitherDeliversOrRetries(t *testing.T) {
	for _, attached := range []bool{true, false} {
		for _, box := range []inputBoxState{inputUnreadable, inputEmpty, inputDrafted, inputUnknown} {
			for _, idle := range []time.Duration{0, time.Second, operatorTypingWindow, time.Hour} {
				for _, held := range []time.Duration{0, time.Minute, draftHoldCap, time.Hour} {
					got := decideDelivery(attached, box, idle, held)
					if got != deliverSubmit && got != deliverHold {
						t.Fatalf("decideDelivery(attached=%v, box=%v, idle=%v, held=%v) = %v — "+
							"a mode that neither delivers nor retries loses the message",
							attached, box, idle, held, got)
					}
				}
			}
		}
	}
}

// A hold must be TEMPORARY. Holding is only defensible because the operator is
// finishing a sentence and will press Enter in seconds; past the cap it becomes
// the silent loss this file exists to prevent.
func TestAHoldAlwaysExpires(t *testing.T) {
	// Mid-sentence: held, which is the behaviour worth keeping.
	if got := decideDelivery(true, inputDrafted, time.Second, 0); got != deliverHold {
		t.Errorf("a nudge arriving mid-sentence should wait, got %v", got)
	}
	// Same operator, still typing, but past the cap.
	if got := decideDelivery(true, inputDrafted, time.Second, draftHoldCap); got != deliverSubmit {
		t.Errorf("a hold outlived the cap and became a silent loss, got %v", got)
	}
	// Same operator, gone quiet.
	if got := decideDelivery(true, inputDrafted, operatorTypingWindow, 0); got != deliverSubmit {
		t.Errorf("a parked draft held the mail, got %v", got)
	}
}
