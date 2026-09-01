package main

import (
	"fmt"
	"strings"
	"testing"
)

// A failed island-image build must carry docker's own words.
//
// The TUI passed io.Discard, so a failure surfaced as
//
//	✗ build island image: docker build failed: exit status 1
//
// and the line naming the cause went nowhere. The operator who hits this is
// usually not the one who can read the daemon's logs — a remote user on their
// own machine — so an exit code is where the investigation stops.
func TestBuildTailKeepsTheEnd(t *testing.T) {
	b := newBuildTail(3)
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(b, "step %d\n", i)
	}
	got := b.String()
	if !strings.Contains(got, "step 10") {
		t.Errorf("the LAST line is missing, which is where a build failure explains "+
			"itself:\n%s", got)
	}
	if strings.Contains(got, "step 1\n") {
		t.Errorf("kept more than the tail — this runs inside a TUI for the length of a "+
			"multi-minute build:\n%s", got)
	}
	if n := len(strings.Split(got, "\n")); n != 3 {
		t.Errorf("kept %d lines, want 3", n)
	}
}

// A build that dies MID-LINE is the interesting case: docker's last write is
// often unterminated, and that fragment is the error.
func TestBuildTailKeepsAnUnterminatedFinalLine(t *testing.T) {
	b := newBuildTail(5)
	fmt.Fprint(b, "downloading\n")
	fmt.Fprint(b, "E: Unable to locate package qemu-user-static") // no newline
	if got := b.String(); !strings.Contains(got, "Unable to locate package") {
		t.Errorf("the unterminated final line was dropped — that is the error itself:\n%s", got)
	}
}

// Written to in arbitrary chunks by an io.Copy, not line by line.
func TestBuildTailReassemblesSplitWrites(t *testing.T) {
	b := newBuildTail(5)
	for _, chunk := range []string{"E: Unab", "le to loc", "ate package foo\n"} {
		fmt.Fprint(b, chunk)
	}
	if got := b.String(); !strings.Contains(got, "E: Unable to locate package foo") {
		t.Errorf("a line split across writes was not reassembled: %q", got)
	}
}

// Blank lines are dropped so the tail is N lines of CONTENT. A build that ends
// with whitespace would otherwise report an empty tail and read as "no output".
func TestBuildTailSkipsBlankLines(t *testing.T) {
	b := newBuildTail(3)
	fmt.Fprint(b, "the real error\n\n\n\n")
	if got := b.String(); !strings.Contains(got, "the real error") {
		t.Errorf("trailing blank lines pushed the error out of the tail: %q", got)
	}
}
