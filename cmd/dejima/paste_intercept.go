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

// pasteScanner sits on the session's raw stdin stream and detects a drag-dropped
// LOCAL file — delivered by the terminal as a bracketed paste whose content is a
// single existing client-local file path. On detection it surfaces the path (via
// the onDrop callback in process) and SWALLOWS the paste, so the caller can
// upload the file and inject the in-island path instead. Everything else passes
// through BYTE-EXACT: this is on the keystroke path, so a non-drop must never be
// altered. State is carried across process() calls (a paste can span reads).
type pasteScanner struct {
	inPaste bool
	buf     []byte // content buffered while inside a (possible) bracketed paste
}

// process feeds raw stdin bytes through the scanner. It returns the bytes to
// forward to the agent (byte-exact for everything that isn't a swallowed file
// drop) and calls onDrop(localPath) for each detected dropped local file. Bytes
// that belong to an incomplete sequence are held internally and emitted later.
func (s *pasteScanner) process(in []byte, onDrop func(localPath string)) []byte {
	data := append(s.buf, in...)
	s.buf = nil
	var out []byte

	for len(data) > 0 {
		if !s.inPaste {
			i := indexOf(data, bpStart)
			if i < 0 {
				// No start marker. Forward everything except a trailing run that
				// could be the beginning of a split bpStart (hold it for next call).
				keep := splitPartialSuffix(data, bpStart)
				out = append(out, data[:len(data)-keep]...)
				s.buf = append(s.buf, data[len(data)-keep:]...)
				return out
			}
			// Forward bytes before the marker verbatim; consume the marker.
			out = append(out, data[:i]...)
			data = data[i+len(bpStart):]
			s.inPaste = true
			continue
		}

		// Inside a paste: look for the end marker.
		j := indexOf(data, bpEnd)
		if j < 0 {
			// Incomplete. If it's already too big or multi-line, it's not a single
			// path — abort detection: re-emit the start marker + what we have and
			// fall back to verbatim passthrough (the eventual end marker just flows
			// through as ordinary bytes), keeping output byte-exact.
			if len(data) > maxPasteScan || hasNewline(data) {
				out = append(out, []byte(bpStart)...)
				out = append(out, data...)
				s.inPaste = false
				return out
			}
			// Hold the content (it may be a path still arriving). Also avoid
			// splitting a trailing partial bpEnd — keep it all buffered.
			s.buf = append(s.buf, data...)
			return out
		}

		content := data[:j]
		data = data[j+len(bpEnd):]
		s.inPaste = false
		if path, ok := droppedLocalFile(content); ok {
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

// droppedLocalFile reports whether content is a single existing client-local
// regular file path (after stripping surrounding quotes and a file:// scheme),
// returning the cleaned path. Multi-path / non-existent / directory → not a drop.
func droppedLocalFile(content []byte) (string, bool) {
	p := strings.TrimSpace(string(content))
	if p == "" || strings.ContainsAny(p, "\n\r") {
		return "", false
	}
	p = strings.TrimSpace(strings.Trim(p, `"'`))
	p = strings.TrimPrefix(p, "file://")
	if p == "" {
		return "", false
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

// indexOf returns the first index of sub in b, or -1.
func indexOf(b []byte, sub string) int { return strings.Index(string(b), sub) }

func hasNewline(b []byte) bool { return strings.ContainsAny(string(b), "\n\r") }

// splitPartialSuffix returns how many trailing bytes of b form a (non-full)
// prefix of marker — bytes to hold back so a marker split across reads isn't
// missed. 0 when no trailing prefix.
func splitPartialSuffix(b []byte, marker string) int {
	max := len(marker) - 1
	if max > len(b) {
		max = len(b)
	}
	for n := max; n > 0; n-- {
		if string(b[len(b)-n:]) == marker[:n] {
			return n
		}
	}
	return 0
}
