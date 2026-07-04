package main

import "testing"

func TestAltScreenWatch(t *testing.T) {
	var w altScreenWatch
	if w.active.Load() {
		t.Fatal("should start inactive")
	}

	w.observe([]byte("normal output \x1b[?1049h now full-screen"))
	if !w.active.Load() {
		t.Error("1049h should activate")
	}

	w.observe([]byte("leaving \x1b[?1049l back to shell"))
	if w.active.Load() {
		t.Error("1049l should deactivate")
	}

	// Last transition in a chunk wins.
	w.observe([]byte("\x1b[?1049h ... \x1b[?1049l"))
	if w.active.Load() {
		t.Error("enter-then-leave in one chunk → inactive")
	}
	w.observe([]byte("\x1b[?1049l ... \x1b[?1049h"))
	if !w.active.Load() {
		t.Error("leave-then-enter in one chunk → active")
	}

	// Older variants.
	var w47 altScreenWatch
	w47.observe([]byte("\x1b[?47h"))
	if !w47.active.Load() {
		t.Error("47h variant should activate")
	}
	w47.observe([]byte("\x1b[?1047l"))
	if w47.active.Load() {
		t.Error("1047l variant should deactivate")
	}

	// Plain output never toggles it.
	var wq altScreenWatch
	wq.observe([]byte("just some \x1b[31mred\x1b[0m text, no alt-screen"))
	if wq.active.Load() {
		t.Error("ordinary SGR output must not activate alt-screen")
	}
}

// TestAltScreenWatchSplit: a marker split across two reads is still caught via
// the holdback tail.
func TestAltScreenWatchSplit(t *testing.T) {
	var w altScreenWatch
	w.observe([]byte("abc\x1b[?10")) // partial enter marker at the seam
	if w.active.Load() {
		t.Error("a partial marker must not activate")
	}
	w.observe([]byte("49h rest")) // completes "\x1b[?1049h"
	if !w.active.Load() {
		t.Error("a marker split across reads should be caught")
	}
}
