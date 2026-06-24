package main

import (
	"bytes"
	"testing"
)

// TestSplitOnSummon: the summon chord (Ctrl-\) is detected only when the session
// is summonable, the keystrokes before it are preserved for forwarding, and an
// ordinary chunk passes through untouched.
func TestSplitOnSummon(t *testing.T) {
	cases := []struct {
		name       string
		in         []byte
		summonable bool
		want       []byte
		summon     bool
	}{
		{"plain text, summonable", []byte("ls -la\r"), true, []byte("ls -la\r"), false},
		{"bare chord, summonable", []byte{summonChord}, true, []byte{}, true},
		{"text then chord", []byte("abc\x1c"), true, []byte("abc"), true},
		{"chord then text (rest discarded)", []byte("\x1cxyz"), true, []byte{}, true},
		{"chord ignored when not summonable", []byte("abc\x1c"), false, []byte("abc\x1c"), false},
		{"ctrl-b is not the chord", []byte{0x02}, true, []byte{0x02}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, summon := splitOnSummon(c.in, c.summonable)
			if summon != c.summon {
				t.Errorf("summon = %v, want %v", summon, c.summon)
			}
			if !bytes.Equal(before, c.want) {
				t.Errorf("before = %q, want %q", before, c.want)
			}
		})
	}
}
