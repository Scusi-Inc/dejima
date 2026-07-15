package main

// Footer help tips — a low-key "did you know" line that rotates in the footer.
// The voice-dictation tip is nudged (placed high + repeated) for operators who
// haven't set it up yet, and drops out of the rotation once it's ready.

const (
	// tipRotateTicks: tickMsg fires every 2s, so the footer tip changes ~every 12s.
	tipRotateTicks = 6
	// voiceCheckTicks: re-probe voice readiness ~every 30s (a cheap PATH+stat probe
	// gated off the 2s tick so it isn't run every frame).
	voiceCheckTicks = 15
	// voiceBoostCap: after the voice tip has been shown this many times in a
	// session, ease its boost back to normal rotation (still in the pool, no longer
	// prioritized/repeated) so a veteran who skips voice isn't nagged forever.
	voiceBoostCap = 8
)

const (
	tipVoice    = "🎙 Dictate to an agent with your voice — run `dejima voice install`, then `dejima voice <island>` (local, on-device, no cloud)"
	tipAttach   = "📎 Attach a file to an agent — Ctrl-] in a session, or `dejima attach <island> <path>`"
	tipInvite   = "👥 Invite a teammate — press s → Team & invites (or `dejima token invite`)"
	tipHostTerm = "🖥 Open an uncontained host terminal with [/] (operator-only)"
)

// footerTips builds the rotating tip pool for the current state. General tips are
// always present; the voice tip is surfaced prominently (second slot — high, but
// not first — plus a repeat so it recurs more often) while voice dictation isn't
// set up, and is dropped entirely once it's ready.
func (m tuiModel) footerTips() []string {
	tips := []string{tipAttach, tipInvite}
	if m.hostTerminalsAvailable() {
		tips = append(tips, tipHostTerm)
	}
	if !m.voice.Ready() {
		if m.voiceTipShown < voiceBoostCap {
			tips = insertStringAt(tips, 1, tipVoice) // high, but not the very first shown
			tips = append(tips, tipVoice)            // and a second slot → recurs more often
		} else {
			tips = append(tips, tipVoice) // boost eased: still in the pool, just normal rotation
		}
	}
	return tips
}

// footerTipText returns the tip to show right now, rotating with the tick count.
func (m tuiModel) footerTipText() string {
	tips := m.footerTips()
	if len(tips) == 0 {
		return ""
	}
	return tips[(m.ticks/tipRotateTicks)%len(tips)]
}

// insertStringAt returns s with v inserted at index i (appended if i is past the
// end), without mutating the caller's backing array.
func insertStringAt(s []string, i int, v string) []string {
	if i >= len(s) {
		return append(append([]string{}, s...), v)
	}
	out := make([]string, 0, len(s)+1)
	out = append(out, s[:i]...)
	out = append(out, v)
	out = append(out, s[i:]...)
	return out
}
