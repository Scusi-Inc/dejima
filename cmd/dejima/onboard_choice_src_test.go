package main

import (
	"os"
	"strings"
	"testing"
)

// The router's prompt is println-to-stdout inside a function that also installs
// software, so driving it in a test would mean stubbing the world. Reading the
// source is the honest way to assert WHAT IS OFFERED — the thing that was wrong
// — without pretending to exercise the branches that act on it.
func onboardSourceForTest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("onboard.go")
	if err != nil {
		t.Fatalf("read onboard.go: %v", err)
	}
	return string(b)
}

// arm returns the source from marker to the end of its case/function block —
// enough to assert which keys a branch claims, cheaply.
func arm(src, marker string) string {
	i := strings.Index(src, marker)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest[len(marker):], "\n\tcase "); j >= 0 {
		return rest[:len(marker)+j]
	}
	if j := strings.Index(rest[len(marker):], "\n}"); j >= 0 {
		return rest[:len(marker)+j]
	}
	return rest
}
