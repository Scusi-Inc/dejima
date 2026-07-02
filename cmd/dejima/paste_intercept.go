package main

import (
	"os"
	"strings"
)

// Bracketed-paste markers. A terminal with bracketed paste enabled (the in-island
// TUI turns it on, and that escape flows out to the user's terminal) wraps pasted
// text — including a drag-dropped file's PATH — in these.
const (
	bpStart = "\x1b[200~"
	bpEnd   = "\x1b[201~"
)

// maxPasteScan caps how much of a bracketed paste we buffer while deciding if
// it's a single dropped-file path. A real path is short; anything larger is a
// text paste, so we stop trying to detect and pass it straight through.
const maxPasteScan = 8192

// Paste-key trigger byte sequences: Ctrl-V (0x16) and Alt-V (ESC v). These are
// app-level keystrokes — the only way to bring a clipboard IMAGE in, since the
// remote agent can't see the client's clipboard and no terminal escape carries
// an image.
var (
	keyCtrlV = []byte{0x16}
	keyAltV  = []byte{0x1b, 'v'}
)

const (
	markerNone = iota
	markerPaste
	markerTrigger
)

// pasteScanner sits on the session's raw stdin stream and bridges two things the
// remote agent can't otherwise receive: a drag-dropped LOCAL file (delivered as a
// bracketed paste of its path) and a clipboard IMAGE (the Ctrl-V/Alt-V keystroke).
// On detection it surfaces them via callbacks and SWALLOWS the bytes, so the
// caller can upload + inject the in-island path. Everything else passes through
// BYTE-EXACT: this is on the keystroke path, so a non-match must never be altered.
// State is carried across process() calls (a paste can span reads).
type pasteScanner struct {
	inPaste  bool
	buf      []byte   // content buffered while inside a (possible) bracketed paste
	triggers [][]byte // configured paste-key sequences; empty = no key interception
}

// process feeds raw stdin bytes through the scanner. It returns the bytes to
// forward to the agent (byte-exact for everything not bridged) and:
//   - calls onDrop(localPath) for a detected dropped local file (swallowed);
//   - calls onPasteKey() on a Ctrl-V/Alt-V trigger — if it returns true (an image
//     was pasted) the keystroke is swallowed, else it's forwarded unchanged.
//
// Either callback may be nil. Bytes belonging to an incomplete sequence are held
// internally and emitted later.
func (s *pasteScanner) process(in []byte, onDrop func(localPath string), onPasteKey func() (handled bool)) []byte {
	data := append(s.buf, in...)
	s.buf = nil
	var out []byte

	for len(data) > 0 {
		if !s.inPaste {
			idx, kind, mlen := s.earliest(data)
			if kind == markerNone {
				// Forward everything except a trailing run (len ≥ 2) that could be
				// the start of a split marker; hold that for the next call. The ≥2
				// floor means a lone trailing ESC is forwarded immediately (never
				// held), so pressing Escape isn't delayed.
				keep := s.holdback(data)
				out = append(out, data[:len(data)-keep]...)
				s.buf = append(s.buf, data[len(data)-keep:]...)
				return out
			}
			out = append(out, data[:idx]...)
			seg := data[idx : idx+mlen]
			data = data[idx+mlen:]
			if kind == markerPaste {
				s.inPaste = true
				continue
			}
			// kind == markerTrigger: a Ctrl-V/Alt-V keystroke.
			handled := false
			if onPasteKey != nil {
				handled = onPasteKey()
			}
			if !handled {
				out = append(out, seg...) // forward the keystroke unchanged
			}
			continue
		}

		// Inside a bracketed paste: look for the end marker.
		j := indexOf(data, bpEnd)
		if j < 0 {
			// Incomplete. Too big or multi-line → not a single path; abort detection,
			// re-emit the start marker + content and fall back to verbatim (the end
			// marker later flows through as ordinary bytes), keeping output byte-exact.
			if len(data) > maxPasteScan || hasNewline(data) {
				out = append(out, []byte(bpStart)...)
				out = append(out, data...)
				s.inPaste = false
				return out
			}
			s.buf = append(s.buf, data...) // hold; the path may still be arriving
			return out
		}
		content := data[:j]
		data = data[j+len(bpEnd):]
		s.inPaste = false
		if path, ok := droppedLocalFile(content); ok && onDrop != nil {
			onDrop(path) // swallow the paste; caller uploads + injects the island path
			continue
		}
		// Not a local file → a normal text paste: reconstruct it verbatim.
		out = append(out, []byte(bpStart)...)
		out = append(out, content...)
		out = append(out, []byte(bpEnd)...)
	}
	return out
}

// earliest returns the first index in data of either bpStart (markerPaste) or any
// configured trigger (markerTrigger), with that marker's length. kind=markerNone
// when none is present.
func (s *pasteScanner) earliest(data []byte) (idx, kind, mlen int) {
	best, bestKind, bestLen := -1, markerNone, 0
	if i := indexOf(data, bpStart); i >= 0 {
		best, bestKind, bestLen = i, markerPaste, len(bpStart)
	}
	for _, t := range s.triggers {
		if i := indexOfBytes(data, t); i >= 0 && (best < 0 || i < best) {
			best, bestKind, bestLen = i, markerTrigger, len(t)
		}
	}
	return best, bestKind, bestLen
}

// holdback returns how many trailing bytes of data form a partial (length ≥ 2)
// prefix of bpStart or any trigger — bytes to hold so a marker split across reads
// isn't missed. The ≥2 floor deliberately never holds a lone byte (e.g. a lone
// trailing ESC), trading a (rare) missed split for not delaying single keystrokes.
func (s *pasteScanner) holdback(data []byte) int {
	markers := append([][]byte{[]byte(bpStart)}, s.triggers...)
	keep := 0
	for _, m := range markers {
		hi := len(m) - 1
		if hi > len(data) {
			hi = len(data)
		}
		for n := hi; n >= 2; n-- {
			if string(data[len(data)-n:]) == string(m[:n]) {
				if n > keep {
					keep = n
				}
				break
			}
		}
	}
	return keep
}

// cleanClientPath strips surrounding whitespace, a matching pair of quotes, and a
// file:// scheme from a client-supplied path (a dragged path, or one typed/pasted
// into the attach minibuffer). It does not expand ~ (see expandClientPath) or
// check existence — callers os.Stat the result.
func cleanClientPath(s string) string {
	p := strings.TrimSpace(s)
	p = strings.TrimSpace(strings.Trim(p, `"'`))
	p = strings.TrimPrefix(p, "file://")
	return p
}

// droppedLocalFile reports whether content is a single existing client-local
// regular file path (after stripping surrounding quotes and a file:// scheme),
// returning the cleaned path. Multi-path / non-existent / directory → not a drop.
func droppedLocalFile(content []byte) (string, bool) {
	p := strings.TrimSpace(string(content))
	if p == "" || strings.ContainsAny(p, "\n\r") {
		return "", false
	}
	p = cleanClientPath(p)
	if p == "" {
		return "", false
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

func indexOf(b []byte, sub string) int { return strings.Index(string(b), sub) }
func indexOfBytes(b, sub []byte) int   { return strings.Index(string(b), string(sub)) }
func hasNewline(b []byte) bool         { return strings.ContainsAny(string(b), "\n\r") }

// configuredPasteTriggers selects which paste-key(s) the session intercepts for a
// clipboard image, via DEJIMA_PASTE_KEY: "ctrl-v", "alt-v", "off"/"none", or
// "both" (default). The operator may not know which key their agent uses, so both
// are on by default; "off" disables clipboard-image interception entirely.
func configuredPasteTriggers() [][]byte {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEJIMA_PASTE_KEY"))) {
	case "off", "none":
		return nil
	case "ctrl-v", "ctrl+v", "c-v":
		return [][]byte{keyCtrlV}
	case "alt-v", "alt+v", "m-v":
		return [][]byte{keyAltV}
	default:
		return [][]byte{keyCtrlV, keyAltV}
	}
}
