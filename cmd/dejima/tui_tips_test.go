package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/voicein"
)

// readyStatus / notReadyStatus fabricate a voicein.Status whose Ready() is the
// value we want, without touching the real host.
var readyStatus = voicein.Status{Supported: true, WhisperBin: "whisper", Recorder: "sox", ModelPresent: true}
var notReadyStatus = voicein.Status{Supported: true} // missing whisper/recorder/model → not ready

func TestFooterTipsVoiceNudge(t *testing.T) {
	if readyStatus.Ready() != true || notReadyStatus.Ready() != false {
		t.Fatalf("test fixtures wrong: ready=%v notReady=%v", readyStatus.Ready(), notReadyStatus.Ready())
	}

	// Not set up: the voice tip is present, appears more than once, and is NOT the
	// very first tip shown (high, but not #1).
	m := tuiModel{voice: notReadyStatus}
	tips := m.footerTips()
	count, firstIsVoice := 0, tips[0] == tipVoice
	for _, tp := range tips {
		if tp == tipVoice {
			count++
		}
	}
	if count < 2 {
		t.Errorf("voice tip should recur (weighted) while not set up, got %d occurrences", count)
	}
	if firstIsVoice {
		t.Error("voice tip should be prominent but NOT the first tip shown")
	}
	if tips[1] != tipVoice {
		t.Errorf("voice tip should sit in the second slot, got %q", tips[1])
	}

	// Ready: the voice tip drops out of the rotation entirely.
	m2 := tuiModel{voice: readyStatus}
	for _, tp := range m2.footerTips() {
		if tp == tipVoice {
			t.Error("voice tip should not appear once dictation is ready")
		}
	}

	// After the boost cap: the voice tip stays in the pool but is no longer boosted
	// (single occurrence, not the second slot) — no perma-nag for veterans.
	m3 := tuiModel{voice: notReadyStatus, voiceTipShown: voiceBoostCap}
	eased := m3.footerTips()
	n, secondIsVoice := 0, len(eased) > 1 && eased[1] == tipVoice
	for _, tp := range eased {
		if tp == tipVoice {
			n++
		}
	}
	if n != 1 {
		t.Errorf("eased voice tip should appear exactly once, got %d", n)
	}
	if secondIsVoice {
		t.Error("eased voice tip should no longer occupy the boosted second slot")
	}
}

func TestFooterTipTextRotates(t *testing.T) {
	m := tuiModel{voice: readyStatus} // stable pool (no voice churn)
	pool := m.footerTips()
	if len(pool) < 2 {
		t.Skip("need at least two tips to test rotation")
	}
	// Advancing the tick counter by tipRotateTicks moves to the next tip.
	m.ticks = 0
	first := m.footerTipText()
	m.ticks = tipRotateTicks
	second := m.footerTipText()
	if first == second {
		t.Errorf("tip should advance after %d ticks, stayed %q", tipRotateTicks, first)
	}
	// A full cycle returns to the start.
	m.ticks = tipRotateTicks * len(pool)
	if got := m.footerTipText(); got != first {
		t.Errorf("tip rotation should wrap to the first tip, got %q want %q", got, first)
	}
}

// TestRenderSettingsVoiceRow pins the Voice-dictation settings entry (Task B):
// "not set up · needs …" when unready, "ready ✓" when ready.
func TestRenderSettingsVoiceRow(t *testing.T) {
	mk := func(st voicein.Status) string {
		m := tuiModel{voice: st, settings: &settingsModel{page: settingsTop}}
		return m.renderSettings()
	}
	unready := mk(notReadyStatus)
	if !strings.Contains(unready, "Voice dictation") {
		t.Errorf("settings should list Voice dictation\n%s", unready)
	}
	if !strings.Contains(unready, "not set up") {
		t.Errorf("unready voice row should say 'not set up'\n%s", unready)
	}
	ready := mk(readyStatus)
	if !strings.Contains(ready, "ready") {
		t.Errorf("ready voice row should say 'ready'\n%s", ready)
	}
}

func TestInsertStringAt(t *testing.T) {
	base := []string{"a", "b", "c"}
	got := insertStringAt(base, 1, "X")
	if strings.Join(got, "") != "aXbc" {
		t.Errorf("insertStringAt mid = %v", got)
	}
	if strings.Join(base, "") != "abc" {
		t.Errorf("insertStringAt must not mutate the caller's slice, got %v", base)
	}
	if got := insertStringAt(base, 99, "Z"); strings.Join(got, "") != "abcZ" {
		t.Errorf("insertStringAt past end should append, got %v", got)
	}
}
