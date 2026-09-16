package api

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
)

// Delivering a mail nudge without eating the operator's half-typed message.
//
// THE PROBLEM. tmuxInject writes the nudge into the agent's pane and then sends
// Enter. tmux types wherever the cursor is, so a nudge arriving while the
// operator is mid-sentence lands inside their text AND submits the result. The
// existing turn-boundary gate does not help: it asks whether the AGENT is idle,
// and while the operator types the agent is `waiting-for-input` — idle by that
// measure, at exactly the wrong moment.
//
// WHAT WE CAN AND CANNOT DO, measured against Claude Code v2.1.273 in a real
// tmux pane rather than reasoned about:
//
//   - A bracketed paste NEVER submits. Text lands in the box and stays.
//   - A paste with a LEADING newline inserts that newline literally, so the
//     operator's line survives intact and the notice sits on its own row. A
//     TRAILING newline leaves their cursor on a fresh line below.
//   - Claude Code collapses a paste of 3+ lines into `[Pasted text #1 +N lines]`
//     — inline, absorbing the leading newline. So the notice stays ONE line.
//   - `Ctrl-U` on a two-line draft JOINS the lines instead of clearing them. It
//     corrupts the draft, which is why "save the draft, clear, submit, restore"
//     is not implemented here: the clear step is destructive, and the screen
//     cannot tell a hard newline from a soft wrap, so the save step is lossy.
//     Its failure mode is "we ate your draft", worse than the bug being fixed.
//
// SO: submit when the box is empty, paste when it is not. The only remaining
// question is how long to wait for a draft that may never be finished, and that
// is what client_activity answers.
const (
	// operatorTypingWindow is how recently a keystroke means "mid-sentence, they
	// are about to press Enter themselves" — hold, and the nudge lands right
	// behind their own message.
	//
	// tmux's client_activity tracks INPUT, not output: measured on a live client,
	// it did not advance through 40s of an agent writing to the pane, and it does
	// not advance for the daemon's own send-keys either (so our paste cannot be
	// mistaken for the operator typing). That is what makes it usable here, and
	// it is a real tmux interface rather than a reading of rendered pixels.
	operatorTypingWindow = 45 * time.Second
	// draftHoldCap stops an abandoned draft holding mail forever. A terminal left
	// open with text in it is the ordinary case, not an edge one.
	draftHoldCap = 2 * time.Minute
)

// inputBoxState is what the agent's prompt is holding.
type inputBoxState int

const (
	// inputUnreadable — we have no way to read this agent's prompt (no known
	// glyph for its framework). Distinct from inputUnknown: we did not look,
	// rather than looked and could not tell.
	inputUnreadable inputBoxState = iota
	inputEmpty
	inputDrafted
	// inputUnknown — the framework has a known prompt and we could not find it.
	// Treated as drafted; an unreadable screen is not permission to submit.
	inputUnknown
)

// deliveryMode is how a nudge should reach the agent.
type deliveryMode int

const (
	deliverSubmit deliveryMode = iota // type it and press Enter (today's behaviour)
	deliverPaste                      // bracketed paste, no Enter — draft survives
	deliverHold                       // try again shortly
)

// decideDelivery is the whole policy, kept pure so it can be tested as a table
// rather than through a container.
//
// The ordering matters: an unattended island never consults the screen at all.
// The fragile part — reading a rendered prompt — runs only when a human is
// attached, which is also the only time it can misbehave in front of anyone.
func decideDelivery(attached bool, box inputBoxState, keyboardIdle, heldFor time.Duration) deliveryMode {
	if !attached || box == inputUnreadable {
		return deliverSubmit
	}
	if box == inputEmpty {
		return deliverSubmit
	}
	// A draft is present (or the screen is unreadable, which we treat the same).
	if keyboardIdle >= operatorTypingWindow || heldFor >= draftHoldCap {
		return deliverPaste
	}
	return deliverHold
}

// paneReading is one sample of an agent's pane: the container's clock, when its
// operator last pressed a key, and what the prompt holds.
type paneReading struct {
	now          time.Time
	keyboardIdle time.Duration
	attached     bool
	box          inputBoxState
}

// readPane collects everything the decision needs in ONE exec, then parses in
// Go. The container side stays a dumb dump on purpose: parsing here is what
// lets the real captured frames be test fixtures.
func (s *Server) readPane(ctx context.Context, p *project.Project, a *project.AgentSpec) paneReading {
	glyph := promptGlyphFor(a.Type)
	if a.Tmux == "" || glyph == "" {
		return paneReading{box: inputUnreadable}
	}
	script := "date +%s; echo ---; tmux list-clients -t " + a.Tmux +
		" -F '#{client_activity}' 2>/dev/null; echo ---; tmux capture-pane -t " + a.Tmux + " -p 2>/dev/null"
	stdout, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"sh", "-c", script})
	if err != nil || code != 0 {
		return paneReading{box: inputUnknown}
	}
	return parsePaneReading(stdout, glyph)
}

// parsePaneReading splits the three-section dump. Sections are separated by a
// line of "---"; a missing or malformed section degrades to the conservative
// answer rather than to a confident wrong one.
func parsePaneReading(out, glyph string) paneReading {
	parts := strings.SplitN(out, "\n---\n", 3)
	if len(parts) != 3 {
		return paneReading{box: inputUnknown}
	}
	r := paneReading{box: inputUnknown}

	if secs, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64); err == nil {
		r.now = time.Unix(secs, 0)
	}

	// Newest activity across every client: a session accumulates dead attaches
	// (twelve on a two-agent island, most of them hours stale), so the operator
	// is the MOST RECENT of them, never any particular one.
	var newest int64
	for _, ln := range strings.Split(parts[1], "\n") {
		if v, err := strconv.ParseInt(strings.TrimSpace(ln), 10, 64); err == nil && v > newest {
			newest = v
		}
	}
	if newest > 0 {
		r.attached = true
		if !r.now.IsZero() {
			r.keyboardIdle = r.now.Sub(time.Unix(newest, 0))
		}
	}

	r.box = classifyInputBox(parts[2], glyph)
	return r
}

// classifyInputBox decides whether the prompt holds a draft.
//
// The LAST occurrence of the glyph, not the first: the transcript above can
// contain the same character in the agent's own output, and the input box is
// always the bottom-most one.
//
// A draft can also be multi-line, whose continuation rows carry no glyph — so
// the scan continues past the glyph row until the box's bottom border.
func classifyInputBox(pane, glyph string) inputBoxState {
	lines := strings.Split(pane, "\n")
	last := -1
	for i, ln := range lines {
		if strings.Contains(ln, glyph) {
			last = i
		}
	}
	if last < 0 {
		return inputUnknown
	}
	// The glyph row, minus everything up to and including the glyph itself.
	head := lines[last]
	if i := strings.LastIndex(head, glyph); i >= 0 {
		head = head[i+len(glyph):]
	}
	if strings.TrimSpace(head) != "" {
		return inputDrafted
	}
	for _, ln := range lines[last+1:] {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if isBoxBorder(t) {
			break // reached the bottom of the input box; nothing was in it
		}
		return inputDrafted // a continuation row of a multi-line draft
	}
	return inputEmpty
}

// isBoxBorder reports whether a line is one of the input box's rules, which is
// what ends the draft region. Box-drawing characters only — a line of dashes
// could plausibly be something the operator typed.
func isBoxBorder(s string) bool {
	for _, r := range s {
		if r != '─' && r != '━' && r != '╭' && r != '╮' && r != '╰' && r != '╯' && r != '│' {
			return false
		}
	}
	return len(s) > 0
}

// promptGlyphFor is the character an interactive framework draws its input
// prompt with, or "" when we have not established one.
//
// Empty is the SAFE answer and keeps today's behaviour: an agent whose prompt we
// cannot locate is delivered to exactly as before, rather than being switched to
// a paste-only path on a guess. Codex is deliberately absent — its own answer is
// `codex queue`, which delivers without touching the input box at all.
func promptGlyphFor(agentType string) string {
	h, ok := handlers.Lookup(agentType)
	if !ok {
		return ""
	}
	return h.PromptGlyph
}

// --- the paste path -------------------------------------------------------

// Bracketed-paste markers (DEC 2004). Wrapping the notice in these is what makes
// the receiving TUI treat it as PASTED content: the embedded newlines insert
// literally instead of submitting, which is the entire mechanism here.
const (
	bpStart = "\x1b[200~"
	bpEnd   = "\x1b[201~"
)

// tmuxPaste puts the notice in the prompt and does NOT press Enter.
//
// The newline either side is the measured part. Without the leading one the
// notice lands jammed against whatever the operator was typing
// (`...the sched[📬 2 new messages]`); with it their line survives and the
// notice sits on its own row. The trailing one leaves their cursor on a fresh
// line rather than at the end of our text.
//
// ONE line of content, deliberately: Claude Code collapses a 3-line paste into
// `[Pasted text #1 +N lines]`, which is inline, opaque and absorbs the leading
// newline — every property we are trying to avoid.
//
// The text is also written to be harmless if submitted. The operator will
// sometimes press Enter without noticing it arrived, and
// `📬 2 new message(s) — run: dejima msg poll` is a sensible thing to send an
// agent; a cryptic marker would be noise in their transcript.
func (s *Server) tmuxPaste(ctx context.Context, p *project.Project, a *project.AgentSpec, text string) error {
	if a.Tmux == "" {
		return nil
	}
	body := "\n[" + text + "]\n"
	if _, _, code, err := s.rt.Exec(ctx, p.ContainerName(),
		[]string{"tmux", "send-keys", "-t", a.Tmux, "-l", bpStart + body + bpEnd}); err != nil {
		return err
	} else if code != 0 {
		return errPasteFailed(code)
	}
	return nil
}

type errPasteFailed int

func (e errPasteFailed) Error() string {
	return "tmux send-keys (paste) exit " + strconv.Itoa(int(e))
}

// pastedSet tracks agents whose draft already holds an un-submitted notice.
// Cleared the moment the prompt is observed empty — that is the operator having
// sent their message, taking our notice with it.
type pastedSet struct {
	mu sync.Mutex
	m  map[nudgeKey]bool
}

func newPastedSet() *pastedSet { return &pastedSet{m: map[nudgeKey]bool{}} }

func (p *pastedSet) seen(k nudgeKey) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[k]
}

func (p *pastedSet) mark(k nudgeKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[k] = true
}

func (p *pastedSet) clear(k nudgeKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, k)
}
