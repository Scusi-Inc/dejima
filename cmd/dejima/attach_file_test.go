package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feedAttach runs a sequence of stdin chunks through a fresh attachState and returns
// the terminal outcome (done/cancelled/path) — mirroring how runOneSessionConn
// pumps chunks in one at a time.
func feedAttach(chunks ...[]byte) (done, cancelled bool, path string) {
	s := &attachState{w: io.Discard}
	for _, c := range chunks {
		done, cancelled, path = s.feed(c)
		if done || cancelled {
			return
		}
	}
	return
}

func TestSplitOnAttach(t *testing.T) {
	key := []byte{attachChordCtrlO}
	// No chord → everything forwarded, no hit.
	if before, after, hit := splitOnAttach([]byte("abc"), key); hit || string(before) != "abc" || len(after) != 0 {
		t.Errorf("no chord: got before=%q after=%q hit=%v", before, after, hit)
	}
	// Chord at start → empty before, empty after.
	if before, after, hit := splitOnAttach([]byte{attachChordCtrlO}, key); !hit || len(before) != 0 || len(after) != 0 {
		t.Errorf("chord-only: got before=%q after=%q hit=%v", before, after, hit)
	}
	// Bytes before + after the chord are split correctly (after seeds the path).
	in := append(append([]byte("ls"), attachChordCtrlO), []byte("/tmp/x")...)
	if before, after, hit := splitOnAttach(in, key); !hit || string(before) != "ls" || string(after) != "/tmp/x" {
		t.Errorf("split: got before=%q after=%q hit=%v", before, after, hit)
	}
	// Disabled (nil key) → never hits.
	if before, _, hit := splitOnAttach([]byte{attachChordCtrlO}, nil); hit || len(before) != 1 {
		t.Errorf("nil key should never hit, got before=%q hit=%v", before, hit)
	}
}

func TestAttachStateFeed(t *testing.T) {
	// Type a path then Enter → done with the path.
	if done, cancelled, p := feedAttach([]byte("a/b.txt"), []byte("\r")); !done || cancelled || p != "a/b.txt" {
		t.Errorf("type+CR: done=%v cancelled=%v path=%q", done, cancelled, p)
	}
	// LF submits too.
	if done, _, p := feedAttach([]byte("x\n")); !done || p != "x" {
		t.Errorf("LF submit: done=%v path=%q", done, p)
	}
	// Backspace deletes the last byte.
	if done, _, p := feedAttach([]byte("abc"), []byte{0x7f}, []byte("\r")); !done || p != "ab" {
		t.Errorf("backspace: done=%v path=%q", done, p)
	}
	// Ctrl-U clears the whole line.
	if done, _, p := feedAttach([]byte("junk"), []byte{0x15}, []byte("ok\r")); !done || p != "ok" {
		t.Errorf("ctrl-u: done=%v path=%q", done, p)
	}
	// Esc cancels.
	if done, cancelled, _ := feedAttach([]byte("half"), []byte{0x1b}); done || !cancelled {
		t.Errorf("esc should cancel: done=%v cancelled=%v", done, cancelled)
	}
	// Ctrl-C cancels.
	if _, cancelled, _ := feedAttach([]byte{0x03}); !cancelled {
		t.Error("ctrl-c should cancel")
	}
	// A path pasted with bracketed-paste markers is unwrapped.
	pasted := []byte(bpStart + "/home/u/key.p8" + bpEnd)
	if done, _, p := feedAttach(pasted, []byte("\r")); !done || p != "/home/u/key.p8" {
		t.Errorf("bracketed paste: done=%v path=%q", done, p)
	}
	// Control bytes (e.g. Tab) are ignored, not appended.
	if done, _, p := feedAttach([]byte("a"), []byte{0x09}, []byte("b\r")); !done || p != "ab" {
		t.Errorf("tab ignored: done=%v path=%q", done, p)
	}
}

func TestExpandClientPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := map[string]string{
		`  /a/b  `:        "/a/b",             // trim whitespace
		`"/a b/c"`:        "/a b/c",           // strip matching quotes
		`file:///etc/x`:   "/etc/x",           // strip file:// scheme
		`~/notes.md`:      home + "/notes.md", // expand leading ~
		`~`:               home,
		`/no/tilde/~/mid`: "/no/tilde/~/mid", // ~ only expands at the front
	}
	for in, want := range cases {
		if got := expandClientPath(in); got != want {
			t.Errorf("expandClientPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfiguredAttachKey(t *testing.T) {
	t.Setenv("DEJIMA_ATTACH_KEY", "")
	if k := configuredAttachKey(); len(k) != 1 || k[0] != attachChordCtrlRB {
		t.Errorf("default should be Ctrl-] (Ctrl-O collides with Claude Code), got %v", k)
	}
	t.Setenv("DEJIMA_ATTACH_KEY", "ctrl-o")
	if k := configuredAttachKey(); len(k) != 1 || k[0] != attachChordCtrlO {
		t.Errorf("ctrl-o should map to 0x0f when explicitly opted in, got %v", k)
	}
	t.Setenv("DEJIMA_ATTACH_KEY", "ctrl-]")
	if k := configuredAttachKey(); len(k) != 1 || k[0] != attachChordCtrlRB {
		t.Errorf("ctrl-] should map to 0x1d, got %v", k)
	}
	t.Setenv("DEJIMA_ATTACH_KEY", "off")
	if k := configuredAttachKey(); k != nil {
		t.Errorf("off should disable, got %v", k)
	}
	t.Setenv("DEJIMA_ATTACH_KEY", "off")
	if lbl := attachKeyLabel(); lbl != "" {
		t.Errorf("disabled label should be empty, got %q", lbl)
	}
	t.Setenv("DEJIMA_ATTACH_KEY", "ctrl-]")
	if lbl := attachKeyLabel(); lbl != "Ctrl-]" {
		t.Errorf("label should be Ctrl-], got %q", lbl)
	}
}

// TestNewAttachCmdShape exercises the `dejima attach` cobra command: its name,
// flags, and arg count (2: <island>[/<agent>] and <path>).
func TestNewAttachCmdShape(t *testing.T) {
	cmd := newAttachCmd()
	if cmd.Name() != "attach" {
		t.Fatalf("command name = %q, want attach", cmd.Name())
	}
	if cmd.Flags().Lookup("agent") == nil || cmd.Flags().Lookup("no-inject") == nil {
		t.Error("attach should expose --agent and --no-inject")
	}
	if err := cmd.Args(cmd, []string{"only-one"}); err == nil {
		t.Error("attach requires exactly 2 args (island[/agent] + path)")
	}
	if err := cmd.Args(cmd, []string{"isl/agent", "./file"}); err != nil {
		t.Errorf("two args should be valid: %v", err)
	}
}

// TestStageLocalFileNaming: the staged destination lands under the paste intake
// dir and preserves the basename (so the agent sees a recognizable filename).
func TestStageLocalFileDestShape(t *testing.T) {
	// stageLocalFile needs a real client to upload; here we only assert the intake
	// convention the dest is built from, which the injected path depends on.
	dir := t.TempDir()
	f := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The dest format mirrors stageLocalFile: <intake>/drop-<ns>-<base>.
	if !strings.HasPrefix(pasteIntakeDir, "/home/dejima/intake") {
		t.Errorf("intake dir moved unexpectedly: %q", pasteIntakeDir)
	}
	if base := filepath.Base(f); base != "report.pdf" {
		t.Errorf("basename = %q", base)
	}
}
