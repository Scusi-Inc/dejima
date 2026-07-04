package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feedAll runs the input (optionally split into chunks) through one scanner and
// returns the concatenated forwarded bytes + any dropped paths. No paste-key
// handler (drag-drop only).
func feedAll(s *pasteScanner, chunks ...[]byte) (string, []string) {
	var out strings.Builder
	var drops []string
	for _, c := range chunks {
		out.Write(s.process(c, func(p string, _ []byte) bool { drops = append(drops, p); return true }, nil))
	}
	return out.String(), drops
}

// feedKeys runs input through a scanner with paste-key triggers, returning the
// forwarded bytes and how many times onPasteKey reported "handled". handledImage
// controls whether the simulated clipboard had an image (handled=true swallows).
func feedKeys(s *pasteScanner, handledImage bool, chunks ...[]byte) (string, int) {
	var out strings.Builder
	calls := 0
	for _, c := range chunks {
		out.Write(s.process(c, nil, func() bool { calls++; return handledImage }))
	}
	return out.String(), calls
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
	// Split at every offset. The invariant under ANY chunking: NEVER corrupt the
	// stream — either the drop is cleanly detected (out="prepost") OR it's passed
	// through byte-exact (out==full). The only accepted miss is a split landing
	// right after the lead ESC of bpStart (we don't hold a lone ESC, to avoid
	// delaying the Escape key); even then the output is verbatim, not garbled.
	detected := 0
	for cut := 1; cut < len(full); cut++ {
		s := &pasteScanner{}
		out, drops := feedAll(s, full[:cut], full[cut:])
		switch {
		case out == "prepost" && len(drops) == 1 && drops[0] == f:
			detected++
		case out == string(full) && len(drops) == 0:
			// accepted miss — byte-exact passthrough
		default:
			t.Errorf("cut %d CORRUPTED stream: out=%q drops=%v", cut, out, drops)
		}
	}
	if detected < len(full)-2 { // at most a couple of ESC-split cuts may miss
		t.Errorf("too many missed drops: detected %d of %d cuts", detected, len(full)-1)
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

func TestPasteScanner_TriggerCtrlVImageHandledSwallows(t *testing.T) {
	s := &pasteScanner{triggers: [][]byte{keyCtrlV, keyAltV}}
	// Ctrl-V with an image on the clipboard → swallowed (caller injects the path).
	out, calls := feedKeys(s, true, []byte("ab\x16cd"))
	if out != "abcd" {
		t.Errorf("Ctrl-V should be swallowed when an image is handled: got %q want %q", out, "abcd")
	}
	if calls != 1 {
		t.Errorf("onPasteKey calls = %d, want 1", calls)
	}
}

func TestPasteScanner_TriggerCtrlVNoImageForwarded(t *testing.T) {
	s := &pasteScanner{triggers: [][]byte{keyCtrlV, keyAltV}}
	// No image (handled=false) → the keystroke passes through unchanged.
	out, calls := feedKeys(s, false, []byte("ab\x16cd"))
	if out != "ab\x16cd" {
		t.Errorf("Ctrl-V should pass through when no image: got %q", out)
	}
	if calls != 1 {
		t.Errorf("onPasteKey calls = %d, want 1", calls)
	}
}

func TestPasteScanner_TriggerAltV(t *testing.T) {
	s := &pasteScanner{triggers: [][]byte{keyCtrlV, keyAltV}}
	out, calls := feedKeys(s, true, []byte("x\x1bvy"))
	if out != "xy" || calls != 1 {
		t.Errorf("Alt-V handled: got out=%q calls=%d, want xy/1", out, calls)
	}
}

func TestPasteScanner_AltVSplitAcrossReads_notHeld(t *testing.T) {
	// A lone trailing ESC must be forwarded immediately (not held), so a plain
	// Escape keypress isn't delayed. Here ESC then 'v' arrive split: because we
	// don't hold the lone ESC, this is NOT treated as Alt-V (safe degradation) —
	// the bytes pass through verbatim and no handler fires.
	s := &pasteScanner{triggers: [][]byte{keyCtrlV, keyAltV}}
	out, calls := feedKeys(s, true, []byte("hi\x1b"), []byte("v"))
	if out != "hi\x1bv" {
		t.Errorf("split Alt-V should pass through verbatim (lone ESC not held): got %q", out)
	}
	if calls != 0 {
		t.Errorf("split Alt-V should not fire the handler, got %d calls", calls)
	}
}

func TestPasteScanner_LoneEscNotHeld(t *testing.T) {
	// Pressing Escape (lone 0x1b at end of a read) must be forwarded right away.
	s := &pasteScanner{triggers: [][]byte{keyCtrlV, keyAltV}}
	out, _ := feedKeys(s, true, []byte("\x1b"))
	if out != "\x1b" {
		t.Errorf("lone ESC must forward immediately, got %q", out)
	}
}

func TestPasteScanner_NoTriggersConfigured_CtrlVPassesThrough(t *testing.T) {
	s := &pasteScanner{} // no triggers
	out, drops := feedAll(s, []byte("a\x16b"))
	if out != "a\x16b" || len(drops) != 0 {
		t.Errorf("without triggers, Ctrl-V is a normal byte: got %q drops=%v", out, drops)
	}
}
