package main

import (
	"net/url"
	"os"
	"path/filepath"
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
//   - calls onDrop(localPath, bracketed) for a detected dropped local file; if it
//     returns true the paste is swallowed (upload/confirm), if false the paste is
//     forwarded verbatim as text (the caller declined to ingest it);
//   - calls onPasteKey() on a Ctrl-V/Alt-V trigger — if it returns true (an image
//     was pasted) the keystroke is swallowed, else it's forwarded unchanged.
//
// Either callback may be nil. Bytes belonging to an incomplete sequence are held
// internally and emitted later.
func (s *pasteScanner) process(in []byte, onDrop func(localPath string, bracketed []byte) bool, onPasteKey func() (handled bool)) []byte {
	data := append(s.buf, in...)
	s.buf = nil
	var out []byte

	for len(data) > 0 {
		if !s.inPaste {
			idx, kind, mlen := s.earliest(data)
			if kind == markerNone {
				// An unwrapped drag-drop (macOS Terminal.app types the path rather
				// than pasting it) reaches us as a plain chunk with no markers at
				// all. Only consider a self-contained read: `out` empty means we
				// haven't already emitted part of this burst, so the chunk stands
				// alone and can be judged as a whole.
				if onDrop != nil && len(out) == 0 {
					if path, ok := droppedUnbracketedFile(data); ok {
						if onDrop(path, data) {
							return out // consumed: uploaded, or a confirm was opened
						}
						// declined → fall through and forward it verbatim as text
					}
				}
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
			// Rebuild the original bracketed paste so onDrop can forward it as TEXT
			// when it declines to ingest (returns false) — e.g. a plain path pasted
			// as a reference, or auto-upload disabled / a full-screen TUI attached.
			bracketed := make([]byte, 0, len(bpStart)+len(content)+len(bpEnd))
			bracketed = append(bracketed, bpStart...)
			bracketed = append(bracketed, content...)
			bracketed = append(bracketed, bpEnd...)
			if onDrop(path, bracketed) {
				continue // consumed: uploaded, or a confirm was opened
			}
			// declined → fall through and forward the paste verbatim (as text)
		}
		// Not a local file (or drop declined) → a normal text paste: reconstruct it.
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
	if rest, ok := strings.CutPrefix(p, "file://"); ok {
		// Finder and iTerm2 hand over a URL, so a path with a space arrives as
		// %20 and won't Stat until it's decoded. Keep the raw form if it isn't
		// valid encoding rather than mangling a literal '%' in a filename.
		if dec, err := url.PathUnescape(rest); err == nil {
			rest = dec
		}
		p = rest
	}
	return unescapeShellPath(p)
}

// shellEscaped is the set a terminal backslash-escapes when it drops a path, so
// the result can be typed at a shell. We are not a shell — left escaped, a path
// with a space never Stats and the drop is silently missed (the actual bug on
// macOS Terminal.app, which emits `/Users/me/my\ file.png`).
//
// Only these are unescaped. A blanket "drop every backslash" would corrupt a
// Windows path (C:\Users\me), where the backslash is a separator, not an escape.
const shellEscaped = ` !"#$&'()*,:;<=>?@[]^` + "`" + `{|}~\`

// unescapeShellPath removes backslash escapes a terminal added ahead of shell
// metacharacters, leaving every other backslash intact.
func unescapeShellPath(p string) string {
	if !strings.Contains(p, `\`) {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) && strings.IndexByte(shellEscaped, p[i+1]) >= 0 {
			i++ // skip the backslash, take the escaped byte literally
		}
		b.WriteByte(p[i])
	}
	return b.String()
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

// droppedUnbracketedFile detects a drag-drop that arrives with NO bracketed-paste
// wrapper. macOS Terminal.app inserts a dropped path as if it were typed, so the
// bracketed-paste path above never sees it — which is why "drag a file onto a
// session" landed a local path in the prompt instead of uploading anything.
//
// Detecting a bare run of bytes as a file path is only safe because of what a
// drop looks like versus typing:
//
//   - it is ABSOLUTE. This is the load-bearing check. A relative fragment like
//     "a" could Stat true against the client's cwd, so a single typed letter
//     would vanish into an upload. Requiring an absolute path makes that
//     impossible, and costs nothing: every terminal drops an absolute path.
//   - it arrives in ONE read. Typing delivers a byte per read in raw mode, and
//     one byte is never an absolute path.
//   - it Stats to a regular file the user actually has.
//
// A path the user genuinely typed by hand and then... didn't press Enter on is
// the only false positive left, and the caller still asks before uploading in a
// plain shell (pasteConfirm).
func droppedUnbracketedFile(content []byte) (string, bool) {
	raw := strings.TrimSpace(string(content))
	// Shortest plausible absolute path is "/x"; a lone byte can't qualify.
	if len(raw) < 2 || strings.ContainsAny(raw, "\n\r") {
		return "", false
	}
	// Must LOOK like a path before we touch the filesystem: this runs on every
	// keystroke chunk, so a Stat per burst of ordinary typing is worth avoiding.
	if !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "~") &&
		!strings.HasPrefix(raw, "'") && !strings.HasPrefix(raw, `"`) &&
		!strings.HasPrefix(raw, "file://") {
		return "", false
	}
	p := expandClientPath(raw)
	if !filepath.IsAbs(p) {
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
