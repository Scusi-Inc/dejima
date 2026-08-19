package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// joinBackground only helps at the call sites that use it, and nothing about a
// missing one fails: the hooks in this package finish in microseconds, so an
// unwrapped NewServer passes every run until the day it doesn't and blames some
// other test. Removing the wrapper from a call site is therefore a survivable
// mutation — which is another way of saying the wiring has no test.
//
// This is that test. It reads the package's own sources and requires every
// NewServer in a _test.go file to be wrapped, so call site nineteen is caught
// when it is written rather than when it flakes.

var newServerCall = regexp.MustCompile(`\bNewServer\(`)

func TestEveryTestServerJoinsItsBackgroundWork(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no _test.go files found — this guard is not reading the package it is guarding")
	}

	var unwrapped []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			loc := newServerCall.FindStringIndex(line)
			if loc == nil {
				continue
			}
			// httptest.NewServer is a different constructor with no arrival hook.
			if strings.HasSuffix(line[:loc[0]], "httptest.") {
				continue
			}
			// The wrapper itself, and the two tests that exercise it directly, own
			// their server's lifetime explicitly. This file constructs no server
			// at all — its NewServer occurrences are the string literals the
			// matcher's own control test is built from.
			if f == "background_join_test.go" || f == "background_join_wiring_test.go" {
				continue
			}
			if !strings.Contains(line[:loc[0]], "joinBackground(t, ") {
				unwrapped = append(unwrapped, f+":"+itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(unwrapped) > 0 {
		t.Errorf("these test servers do not join their detached work — wrap them as "+
			"joinBackground(t, NewServer(...)):\n  %s", strings.Join(unwrapped, "\n  "))
	}
}

// The guard has to be able to see an unwrapped call, or it is decoration. This
// asserts the matcher fires on the exact shape it is looking for, so a future
// edit to the regex or the httptest exclusion cannot quietly make the guard
// blind while leaving it green.
func TestJoinWiringGuardRecognisesAnUnwrappedCall(t *testing.T) {
	cases := []struct {
		line string
		want bool // true = the guard should flag it
	}{
		{"\tsrv := NewServer(f, log, nil)", true},
		{"\treturn NewServer(f, log, nil)", true},
		{"\tsrv := joinBackground(t, NewServer(f, log, nil))", false},
		{"\tts := httptest.NewServer(h)", false},
	}
	for _, c := range cases {
		loc := newServerCall.FindStringIndex(c.line)
		flagged := loc != nil &&
			!strings.HasSuffix(c.line[:loc[0]], "httptest.") &&
			!strings.Contains(c.line[:loc[0]], "joinBackground(t, ")
		if flagged != c.want {
			t.Errorf("guard flagged=%v want %v for %q", flagged, c.want, c.line)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
