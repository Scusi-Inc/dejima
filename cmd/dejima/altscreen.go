package main

import (
	"bytes"
	"sync/atomic"
)

// Alt-screen (alternate screen buffer) DECSET/DECRST sequences a full-screen TUI
// emits when it takes over the terminal (…h) and releases it (…l). 1049 is the
// modern one (bubbletea / Claude Code / codex); 1047 and 47 are older variants.
var (
	altScreenEnter = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")}
	altScreenLeave = [][]byte{[]byte("\x1b[?1049l"), []byte("\x1b[?1047l"), []byte("\x1b[?47l")}
)

// maxAltMarkerLen is the length of the longest marker ("\x1b[?1049h"); we hold
// back one byte less than this between reads to catch a marker split at the seam.
const maxAltMarkerLen = 8

// altScreenWatch tracks whether the attached agent currently owns a full-screen
// (alternate-screen) TUI, by scanning its OUTPUT stream for the alt-screen
// enter/leave escapes. When active, the client must not print inline notices or
// prompts into the session — the agent owns the screen + cursor, so any stray
// write corrupts its render (the cursor-desync bug). observe() runs only in the
// output-reader goroutine; active is atomic so other goroutines read it safely.
type altScreenWatch struct {
	active atomic.Bool
	tail   []byte // trailing bytes held so a marker split across reads is still caught
}

// observe scans a chunk of agent output and updates active to the LAST alt-screen
// transition seen — a TUI toggles the whole screen, so the most recent marker in
// the stream wins (enter then leave in one chunk ⇒ inactive, and vice-versa).
func (w *altScreenWatch) observe(b []byte) {
	data := b
	if len(w.tail) > 0 {
		data = append(append([]byte(nil), w.tail...), b...)
	}

	lastPos, lastState, seen := -1, false, false
	for _, m := range altScreenEnter {
		if i := bytes.LastIndex(data, m); i > lastPos {
			lastPos, lastState, seen = i, true, true
		}
	}
	for _, m := range altScreenLeave {
		if i := bytes.LastIndex(data, m); i > lastPos {
			lastPos, lastState, seen = i, false, true
		}
	}
	if seen {
		w.active.Store(lastState)
	}

	// Hold back the trailing bytes that could be the head of a marker split across
	// the read boundary, so the next observe() completes it. (A whole marker can't
	// fit in maxAltMarkerLen-1 bytes of the longest marker, but a shorter one might
	// re-scan harmlessly — Store is idempotent and a later marker always wins.)
	if keep := maxAltMarkerLen - 1; len(data) > keep {
		w.tail = append([]byte(nil), data[len(data)-keep:]...)
	} else {
		w.tail = append([]byte(nil), data...)
	}
}
