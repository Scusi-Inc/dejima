package main

import (
	"testing"
	"time"

	"github.com/aoos/dejima/internal/events"
)

func seedEvent(island string, ago time.Duration, now time.Time, payloadSrc string) events.Event {
	e := events.Event{
		Type:      events.TypeCredentialsAutoSeeded,
		Island:    island,
		Timestamp: now.Add(-ago),
	}
	if payloadSrc != "" {
		e.Payload = map[string]any{"source_island": payloadSrc}
	}
	return e
}

func TestClaudeAutoSeedNotice(t *testing.T) {
	now := time.Now()

	// Fresh, unseen event → notice with the payload source island.
	evs := []events.Event{seedEvent("isl-a", 1*time.Minute, now, "isl-a")}
	note, at, ok := claudeAutoSeedNotice(evs, time.Time{}, now)
	if !ok {
		t.Fatal("a fresh auto-seed event should raise a notice")
	}
	if want := "🔑 Seeded your Claude login from isl-a — new agents skip sign-in."; note != want {
		t.Errorf("note = %q, want %q", note, want)
	}
	if at.IsZero() {
		t.Error("returned timestamp should be the event's")
	}

	// Already surfaced (since >= event time) → no repeat.
	if _, _, ok := claudeAutoSeedNotice(evs, at, now); ok {
		t.Error("an already-surfaced event must not fire again")
	}

	// Stale (older than the window) → not surfaced as a banner.
	old := []events.Event{seedEvent("isl-a", autoSeedNoticeWindow+time.Minute, now, "isl-a")}
	if _, _, ok := claudeAutoSeedNotice(old, time.Time{}, now); ok {
		t.Error("a stale auto-seed event must not raise the banner")
	}

	// Picks the newest among several.
	multi := []events.Event{
		seedEvent("old", 10*time.Minute, now, "old"),
		seedEvent("new", 1*time.Minute, now, "new"),
	}
	if note, _, ok := claudeAutoSeedNotice(multi, time.Time{}, now); !ok || note != "🔑 Seeded your Claude login from new — new agents skip sign-in." {
		t.Errorf("should pick the newest event, got ok=%v note=%q", ok, note)
	}

	// Source falls back to Island when the payload lacks source_island.
	nopayload := []events.Event{seedEvent("isl-b", 1*time.Minute, now, "")}
	if note, _, _ := claudeAutoSeedNotice(nopayload, time.Time{}, now); note != "🔑 Seeded your Claude login from isl-b — new agents skip sign-in." {
		t.Errorf("source should fall back to Island, got %q", note)
	}

	// No auto-seed events → nothing.
	other := []events.Event{{Type: "island.created", Timestamp: now}}
	if _, _, ok := claudeAutoSeedNotice(other, time.Time{}, now); ok {
		t.Error("non-seed events must not raise the notice")
	}
}

func TestEventSummary(t *testing.T) {
	now := time.Now()
	if got := eventSummary(seedEvent("isl-a", 0, now, "isl-a")); got != "Claude login seeded from isl-a" {
		t.Errorf("autoseed summary = %q", got)
	}
	if got := eventSummary(events.Event{Type: "island.created"}); got != "island.created" {
		t.Errorf("other events keep the raw type, got %q", got)
	}
}
