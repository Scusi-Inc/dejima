package main

import "testing"

// The whole reason parseSize exists rather than reusing internal/runtime's
// parseBytes is that junk must be an ERROR, not a zero. Zero means "use the
// daemon's default", so a swallowed parse failure would silently re-request the
// caps that just refused the import — reading to the operator as "the override
// does not work" instead of "you typed the size wrong".
func TestParseSizeRejectsJunkInsteadOfReturningZero(t *testing.T) {
	for _, in := range []string{"", "  ", "2 gigs", "G", "abc", "-1", "1.2.3", "1e400"} {
		if got, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) = %d, nil — junk must not parse as a cap", in, got)
		}
	}
}

func TestParseSizeUnits(t *testing.T) {
	cases := map[string]int64{
		"512":    512,
		"512B":   512,
		"1K":     1 << 10,
		"1KiB":   1 << 10,
		"1KB":    1000,
		"512MiB": 512 << 20,
		"2G":     2 << 30,
		"2GiB":   2 << 30,
		"1.5GiB": 1610612736,
		"2gb":    2_000_000_000,
		"  2G  ": 2 << 30,
		"1T":     1 << 40,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

// "GiB" must not be matched by the "B" rule, and "2G" must not be read as 2.
// Both misreads produce a cap far SMALLER than asked for, so the import is
// refused again and the flag looks broken.
func TestParseSizeLongestSuffixWins(t *testing.T) {
	gib, _ := parseSize("1GiB")
	if gib != 1<<30 {
		t.Errorf("1GiB = %d, want %d — a shorter suffix matched first", gib, int64(1)<<30)
	}
	if b, _ := parseSize("1B"); b != 1 {
		t.Errorf("1B = %d, want 1", b)
	}
}
