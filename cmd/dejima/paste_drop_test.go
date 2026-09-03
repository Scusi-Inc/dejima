package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The site promises "drag a file onto a session and it uploads into the island."
// It didn't: detection only ran inside a bracketed paste, and macOS Terminal.app
// inserts a dropped path as if TYPED — no markers — so a local path landed in the
// prompt instead. These are the shapes real terminals actually emit.
func TestDropDetection_RealTerminalFormats(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "shot.png")
	spaced := filepath.Join(dir, "my file.png")
	for _, p := range []string{plain, spaced} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare absolute path", plain, plain},
		{"trailing space (Terminal.app appends one)", plain + " ", plain},
		{"backslash-escaped space (Terminal.app)", dir + `/my\ file.png`, spaced},
		{"single-quoted (iTerm2)", "'" + spaced + "'", spaced},
		{"double-quoted", `"` + spaced + `"`, spaced},
		{"file:// URL", "file://" + plain, plain},
		{"file:// URL, percent-encoded (Finder)", "file://" + dir + "/my%20file.png", spaced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := droppedUnbracketedFile([]byte(tc.in))
			if !ok {
				t.Fatalf("not detected as a drop: %q", tc.in)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			// The bracketed path must agree — a terminal that DOES wrap its drops
			// (iTerm2) has to resolve to the same file.
			if bgot, bok := droppedLocalFile([]byte(tc.in)); !bok || bgot != tc.want {
				t.Errorf("bracketed detection disagrees: ok=%v got=%q want=%q", bok, bgot, tc.want)
			}
		})
	}
}

// The safety property. Unbracketed detection reads raw keystrokes, so anything it
// swallows by mistake is input the user typed and never sees again. A relative
// name is the dangerous case: it can Stat true against the client's cwd.
func TestDropDetection_NeverSwallowsTyping(t *testing.T) {
	dir := t.TempDir()
	// A file whose RELATIVE name is a single character — the worst case: if
	// relative paths were accepted, typing "a" would vanish into an upload.
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	for _, in := range []string{
		"a",                     // the single keystroke that names a real file
		"./a",                   // explicitly relative
		"a ",                    // with the trailing space a drop would have
		"ls -la",                // ordinary command
		"/etc",                  // a directory, not a regular file
		"/nope/missing.png",     // absolute but nonexistent
		"cat /etc/hosts",        // an absolute path embedded in a command
		"/etc/hosts /etc/hosts", // two paths — not a single drop
		"",                      // empty
		"/",                     // root
	} {
		if p, ok := droppedUnbracketedFile([]byte(in)); ok {
			t.Errorf("input %q was swallowed as a drop (resolved to %q) — this is typed input the user loses", in, p)
		}
	}
}

// Everything the scanner doesn't bridge must pass through byte-for-byte; it sits
// directly on the keystroke path.
func TestPasteScanner_TypingIsByteExact(t *testing.T) {
	s := &pasteScanner{}
	var dropped []string
	onDrop := func(p string, _ []byte) bool { dropped = append(dropped, p); return true }

	var got []byte
	for _, chunk := range []string{"l", "s", " ", "-", "l", "a", "\r"} {
		got = append(got, s.process([]byte(chunk), onDrop, nil)...)
	}
	if string(got) != "ls -la\r" {
		t.Errorf("typing was altered: got %q, want %q", got, "ls -la\r")
	}
	if len(dropped) != 0 {
		t.Errorf("typing triggered a drop: %v", dropped)
	}
}

// A drop the caller declines (pasteAsText, or an upload failure) must reach the
// agent as the original text, unchanged.
func TestPasteScanner_DeclinedDropForwardsVerbatim(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &pasteScanner{}
	out := s.process([]byte(p), func(string, []byte) bool { return false }, nil)
	if string(out) != p {
		t.Errorf("declined drop was not forwarded verbatim:\n got %q\nwant %q", out, p)
	}
}

// An accepted unbracketed drop is swallowed — the agent must not also receive the
// local path, which is meaningless inside the island.
func TestPasteScanner_AcceptedDropIsSwallowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &pasteScanner{}
	var seen string
	out := s.process([]byte(p), func(lp string, _ []byte) bool { seen = lp; return true }, nil)
	if seen != p {
		t.Errorf("onDrop got %q, want %q", seen, p)
	}
	if len(out) != 0 {
		t.Errorf("accepted drop leaked the local path to the agent: %q", out)
	}
}

// Unescaping is scoped to shell metacharacters so a Windows path survives: there
// the backslash is a separator, and stripping it would destroy the path.
func TestUnescapeShellPath_PreservesWindowsSeparators(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`C:\Users\amanda\notes.md`, `C:\Users\amanda\notes.md`},
		{`/Users/me/my\ file.png`, `/Users/me/my file.png`},
		{`/Users/me/a\(1\).png`, `/Users/me/a(1).png`},
		{`/Users/me/plain.png`, `/Users/me/plain.png`},
	} {
		if got := unescapeShellPath(tc.in); got != tc.want {
			t.Errorf("unescapeShellPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The claim on the site is the spec. If the drop pipeline is ever gated back
// behind bracketed paste, the promise silently breaks again.
func TestScannerConsultsDropDetectionWithoutMarkers(t *testing.T) {
	body, err := os.ReadFile("paste_intercept.go")
	if err != nil {
		t.Fatalf("read paste_intercept.go: %v", err)
	}
	src := string(body)
	marker := strings.Index(src, "if kind == markerNone {")
	if marker < 0 {
		t.Fatal("no-marker branch not found — this guard is scanning the wrong place")
	}
	end := marker + 1200
	if end > len(src) {
		end = len(src)
	}
	if !strings.Contains(src[marker:end], "droppedUnbracketedFile") {
		t.Error("the no-marker branch no longer checks for an unbracketed drop — " +
			"macOS Terminal.app types the path instead of pasting it, so drag-drop breaks")
	}
}
