package srcscan

import (
	"strings"
	"testing"
)

// The thing this package exists for: a guard searching for a token must not be
// satisfied by a comment that names it.
func TestGoCommentsCannotSatisfyASearch(t *testing.T) {
	src := `package main

// We deliberately call dangerousThing() here because …
func f() {
	safeThing()
}
`
	out, ok := StripGoComments(src)
	if !ok {
		t.Fatal("valid Go should strip cleanly")
	}
	if strings.Contains(out, "dangerousThing") {
		t.Errorf("the comment's mention survived — a guard would match prose:\n%s", out)
	}
	if !strings.Contains(out, "safeThing()") {
		t.Errorf("real code was removed:\n%s", out)
	}
}

// THE CONTROL THAT MATTERS. Every other test here asserts something is gone,
// and a function that returned "" would pass all of them. This one proves the
// stripper is not simply eating the file — which is the failure that would
// reintroduce, inside the fix, the exact bug the fix is for.
func TestStripperDoesNotSilentlyEatCode(t *testing.T) {
	src := `package main

func f() {
	callOne()
	callTwo()
	callThree()
}
`
	out, ok := StripGoComments(src)
	if !ok {
		t.Fatal("valid Go should strip cleanly")
	}
	for _, want := range []string{"package main", "func f()", "callOne()", "callTwo()", "callThree()"} {
		if !strings.Contains(out, want) {
			t.Errorf("comment-free source lost %q — the stripper is removing code:\n%s", want, out)
		}
	}
	if len(out) != len(src) {
		t.Errorf("length changed: got %d, want %d — offsets into the result are no longer valid", len(out), len(src))
	}
}

// A comment marker inside a string literal is not a comment. Getting this wrong
// removes real code, which is the dangerous direction.
func TestGoStringLiteralsSurvive(t *testing.T) {
	src := `package main

func f() {
	url := "https://example.com/x" // trailing comment mentioning secretToken
	re := "// not a comment"
	_ = url
	_ = re
}
`
	out, ok := StripGoComments(src)
	if !ok {
		t.Fatal("valid Go should strip cleanly")
	}
	if !strings.Contains(out, `"https://example.com/x"`) {
		t.Errorf("a URL containing // was truncated:\n%s", out)
	}
	if !strings.Contains(out, `"// not a comment"`) {
		t.Errorf("a string literal that looks like a comment was stripped:\n%s", out)
	}
	if strings.Contains(out, "secretToken") {
		t.Errorf("the trailing comment survived:\n%s", out)
	}
}

// Line structure has to survive, or a guard that reports a line number sends
// the reader to the wrong place.
func TestGoLineNumbersPreserved(t *testing.T) {
	src := "package main\n\n/* one\ntwo\nthree */\nfunc f() {}\n"
	out, ok := StripGoComments(src)
	if !ok {
		t.Fatal("valid Go should strip cleanly")
	}
	if got, want := strings.Count(out, "\n"), strings.Count(src, "\n"); got != want {
		t.Errorf("newline count changed: got %d, want %d", got, want)
	}
	if strings.Contains(out, "two") {
		t.Errorf("block comment body survived:\n%s", out)
	}
}

// Unparseable input must be reported, not silently half-processed: a partially
// stripped file is the one state that could hide a real match.
func TestUnparseableGoIsReportedNotGuessed(t *testing.T) {
	src := "package main\nfunc f() { s := \"unterminated\n}\n"
	out, ok := StripGoComments(src)
	if ok {
		t.Error("a lexical error must not report success")
	}
	if out != src {
		t.Error("on failure the input must come back unchanged, so the caller scans the raw file")
	}
}

func TestLineCommentsStrippedWholeLineOnly(t *testing.T) {
	src := "" +
		"set -e\n" +
		"# verify the checksum with sha256sum -c before installing\n" +
		"    # indented comment mentioning sha256sum -c too\n" +
		"curl -fsSL https://example.com/x -o /root/x\n"

	out := StripLineComments(src, "#")
	if strings.Count(out, "sha256sum -c") != 0 {
		t.Errorf("prose describing the command survived — the guard could match it:\n%s", out)
	}
	if !strings.Contains(out, "curl -fsSL") || !strings.Contains(out, "set -e") {
		t.Errorf("real script lines were removed:\n%s", out)
	}
	if got, want := strings.Count(out, "\n"), strings.Count(src, "\n"); got != want {
		t.Errorf("line count changed: got %d, want %d", got, want)
	}
}

// The documented conservative choice, asserted so nobody "improves" it into a
// mid-line stripper without reading why. A trailing comment costs a false
// positive; guessing wrong about a quoted marker costs a missed match.
func TestTrailingCommentsAreDeliberatelyKept(t *testing.T) {
	src := "echo hello   # this trailing note stays\n"
	out := StripLineComments(src, "#")
	if !strings.Contains(out, "this trailing note stays") {
		t.Error("trailing comments must be kept — stripping them needs a parser, " +
			"and guessing wrong removes real code")
	}
}

func TestLineCommentsNeverRemoveCodeThatContainsTheMarker(t *testing.T) {
	src := "curl 'https://example.com/#anchor'\ngrep '#define' file\n"
	out := StripLineComments(src, "#")
	if !strings.Contains(out, "#anchor") || !strings.Contains(out, "#define") {
		t.Errorf("a marker inside a quoted argument was treated as a comment:\n%s", out)
	}
}
