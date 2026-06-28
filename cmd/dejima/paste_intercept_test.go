package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feedAll runs the input (optionally split into chunks) through one scanner and
// returns the concatenated forwarded bytes + any dropped paths.
func feedAll(s *pasteScanner, chunks ...[]byte) (string, []string) {
	var out strings.Builder
	var drops []string
	for _, c := range chunks {
		out.Write(s.process(c, func(p string) { drops = append(drops, p) }))
	}
	return out.String(), drops
}

func TestPasteScanner_PlainBytesAreByteExact(t *testing.T) {
	s := &pasteScanner{}
	in := []byte("hello \x1b[A world\x03\r\n$ ls -la\t")
	out, drops := feedAll(s, in)
	if out != string(in) {
		t.Errorf("plain bytes not byte-exact:\n got %q\nwant %q", out, in)
	}
	if len(drops) != 0 {
		t.Errorf("unexpected drops: %v", drops)
	}
}

func TestPasteScanner_TextPasteForwardedIntact(t *testing.T) {
	s := &pasteScanner{}
	paste := bpStart + "just some pasted text, not a path" + bpEnd
	out, drops := feedAll(s, []byte("a"+paste+"b"))
	if out != "a"+paste+"b" {
		t.Errorf("text paste not preserved:\n got %q\nwant %q", out, "a"+paste+"b")
	}
	if len(drops) != 0 {
		t.Errorf("text paste should not drop: %v", drops)
	}
}

func TestPasteScanner_NonexistentPathForwarded(t *testing.T) {
	s := &pasteScanner{}
	paste := bpStart + "/no/such/file/here.png" + bpEnd
	out, drops := feedAll(s, []byte(paste))
	if out != paste {
		t.Errorf("nonexistent path should pass through verbatim, got %q", out)
	}
	if len(drops) != 0 {
		t.Errorf("nonexistent path should not drop: %v", drops)
	}
}

func TestPasteScanner_DroppedFileDetectedAndSwallowed(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(f, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plain path, and quoted (terminals quote paths with spaces) — both detect.
	for _, payload := range []string{f, `"` + f + `"`, "file://" + f} {
		s := &pasteScanner{}
		out, drops := feedAll(s, []byte("x"+bpStart+payload+bpEnd+"y"))
		if out != "xy" {
			t.Errorf("payload %q: drop not swallowed, surrounding bytes lost: got %q want %q", payload, out, "xy")
		}
		if len(drops) != 1 || drops[0] != f {
			t.Errorf("payload %q: drops = %v, want [%s]", payload, drops, f)
		}
	}
}

func TestPasteScanner_SplitAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.png")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	full := []byte("pre" + bpStart + f + bpEnd + "post")
	// Split at every offset; the result must be identical regardless of chunking.
	for cut := 1; cut < len(full); cut++ {
		s := &pasteScanner{}
		out, drops := feedAll(s, full[:cut], full[cut:])
		if out != "prepost" || len(drops) != 1 || drops[0] != f {
			t.Errorf("cut %d: out=%q drops=%v, want prepost + [%s]", cut, out, drops, f)
		}
	}
}

func TestPasteScanner_MultilinePasteNotADrop(t *testing.T) {
	s := &pasteScanner{}
	paste := bpStart + "line one\nline two\n" + bpEnd
	out, drops := feedAll(s, []byte(paste))
	if out != paste {
		t.Errorf("multiline paste should pass through verbatim:\n got %q\nwant %q", out, paste)
	}
	if len(drops) != 0 {
		t.Errorf("multiline should not drop: %v", drops)
	}
}

func TestPasteScanner_HugePasteAbortsToVerbatim(t *testing.T) {
	s := &pasteScanner{}
	big := strings.Repeat("a", maxPasteScan+100)
	paste := bpStart + big + bpEnd
	out, _ := feedAll(s, []byte(paste))
	if out != paste {
		t.Errorf("oversized paste not preserved byte-exact (len got %d want %d)", len(out), len(paste))
	}
}

func TestPasteScanner_StartMarkerSplitHeldBack(t *testing.T) {
	// A lone partial start marker at end of a chunk must not be emitted early nor
	// lost; once completed (as a text paste) it round-trips verbatim.
	s := &pasteScanner{}
	out, drops := feedAll(s, []byte("hi\x1b[2"), []byte("00~text"+bpEnd))
	want := "hi" + bpStart + "text" + bpEnd
	if out != want {
		t.Errorf("split start marker:\n got %q\nwant %q", out, want)
	}
	if len(drops) != 0 {
		t.Errorf("unexpected drops: %v", drops)
	}
}
