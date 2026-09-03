package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cpTree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "a.txt"), "aaa")
	write(filepath.Join(src, "sub", "b.txt"), "bbbb")
	write(filepath.Join(outside, "secret"), "SECRET")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return src
}

// The same symlink policy as the brokered path, for the same reason: copying
// through a link either duplicates content or drags in something the user only
// meant to reference. `cp -r` has no scope check behind it, so the walk is the
// only thing standing between "copy this folder" and "copy that too".
func TestWalkLocalTree_SkipsSymlinksAndReportsThem(t *testing.T) {
	src := cpTree(t)

	files, skipped, err := walkLocalTree(src)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	if strings.Join(got, ",") != "a.txt,sub/b.txt" {
		t.Errorf("files = %v, want the two regular files", got)
	}
	if len(skipped) != 1 || skipped[0].rel != "link" {
		t.Fatalf("skipped = %+v, want the symlink reported", skipped)
	}
	if !strings.Contains(skipped[0].reason, "symlink") {
		t.Errorf("reason = %q, want it to name the symlink", skipped[0].reason)
	}
}

// Sizes ride along so the byte cap is answerable before anything is sent.
func TestWalkLocalTree_CarriesSizes(t *testing.T) {
	src := cpTree(t)
	files, _, err := walkLocalTree(src)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	if total != 7 { // "aaa" + "bbbb"
		t.Errorf("total = %d, want 7 — without sizes the cap could only be applied after copying", total)
	}
}

func TestCheckCpCaps(t *testing.T) {
	many := make([]cpEntry, cpMaxFiles+1)
	err := checkCpCaps(many, "./huge")
	if err == nil {
		t.Fatal("a copy over the file cap was allowed")
	}
	// The numbers have to be in the message: a refusal that doesn't say how far
	// over you are just prompts a guess and a retry.
	if !strings.Contains(err.Error(), "2001") || !strings.Contains(err.Error(), "2000") {
		t.Errorf("refusal should carry both the count and the limit, got: %v", err)
	}

	big := []cpEntry{{rel: "x", size: cpMaxBytes + 1}}
	if err := checkCpCaps(big, "./huge"); err == nil {
		t.Error("a copy over the byte cap was allowed")
	}

	if err := checkCpCaps([]cpEntry{{rel: "x", size: 10}}, "./small"); err != nil {
		t.Errorf("an ordinary copy was refused: %v", err)
	}
}

// "I copied nothing" and "there was nothing to copy" are different sentences.
// A directory of only symlinks reads as an empty directory once they are
// skipped, so the refusal must name the cause.
func TestEmptyCopyError_ExplainsWhyItFoundNothing(t *testing.T) {
	err := emptyCopyError("./notes", []cpSkip{{"a", "symlink (never followed)"}, {"b", "symlink (never followed)"}})
	if err == nil {
		t.Fatal("an empty copy reported no error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal should say WHY nothing matched, got: %v", err)
	}

	bare := emptyCopyError("./empty", nil)
	if bare == nil || strings.Contains(bare.Error(), "skipped") {
		t.Errorf("a genuinely empty directory should not claim it skipped anything, got: %v", bare)
	}
}

// A partial copy must not exit 0, and must not claim anything was undone. There
// is no rollback here either — removing files that already landed, to tidy up a
// failure, is how work gets destroyed.
func TestCpResultReport_PartialFailureIsAnError(t *testing.T) {
	ok := cpResult{copied: 2, bytes: 7}
	if err := ok.report("./src", "isl:/dst"); err != nil {
		t.Errorf("a clean copy returned an error: %v", err)
	}

	partial := cpResult{copied: 2, bytes: 7, failed: []cpSkip{{"c.txt", "permission denied"}}}
	err := partial.report("./src", "isl:/dst")
	if err == nil {
		t.Fatal("a partial copy reported success — the caller would believe the whole folder arrived")
	}
	if !strings.Contains(err.Error(), "nothing was removed") {
		t.Errorf("the error should say the copied files were kept, got: %v", err)
	}
}
