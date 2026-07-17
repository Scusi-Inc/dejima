package main

import (
	"time"

	"github.com/aoos/dejima/internal/events"
)

// autoSeedNoticeWindow bounds how fresh a Claude auto-seed event must be to raise
// the prominent one-time notice. Older ones still render in the island's Recent
// feed; only a just-happened capture earns the banner (so it reads as "this just
// occurred", and stale events don't re-nag on a later island visit).
const autoSeedNoticeWindow = 15 * time.Minute

// claudeAutoSeedNotice scans events for the newest Claude credential auto-seed
// that is both newer than the last one surfaced (since) and recent enough to
// announce, returning a human notice + that event's timestamp. Makes a surprise
// credential capture VISIBLE exactly once — the daemon captured the operator's
// Claude login from an island, and every new agent now skips sign-in.
func claudeAutoSeedNotice(evs []events.Event, since, now time.Time) (string, time.Time, bool) {
	var newest events.Event
	found := false
	for _, e := range evs {
		if e.Type != events.TypeCredentialsAutoSeeded {
			continue
		}
		if !e.Timestamp.After(since) || now.Sub(e.Timestamp) > autoSeedNoticeWindow {
			continue
		}
		if !found || e.Timestamp.After(newest.Timestamp) {
			newest, found = e, true
		}
	}
	if !found {
		return "", since, false
	}
	note := "🔑 Seeded your Claude login"
	if src := autoSeedSource(newest); src != "" {
		note += " from " + src
	}
	note += " — new agents skip sign-in."
	return note, newest.Timestamp, true
}

// autoSeedSource resolves the island the login was captured from: the event's
// Payload["source_island"] if present, else its Island field.
func autoSeedSource(e events.Event) string {
	if s, ok := e.Payload["source_island"].(string); ok && s != "" {
		return s
	}
	return e.Island
}

// eventSummary renders a human line for an event in the Recent feed, special-
// casing the ones worth a friendly phrasing; everything else keeps the raw type.
func eventSummary(e events.Event) string {
	switch e.Type {
	case events.TypeCredentialsAutoSeeded:
		if src := autoSeedSource(e); src != "" {
			return "Claude login seeded from " + src
		}
		return "Claude login seeded"
	default:
		return string(e.Type)
	}
}
