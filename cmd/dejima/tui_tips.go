package main

// Footer help tips — a low-key "did you know" line that rotates in the footer.

const (
	// tipRotateTicks: tickMsg fires every 2s, so the footer tip changes ~every 12s.
	tipRotateTicks = 6
)

const (
	tipAttach   = "📎 Attach a file to an agent — Ctrl-] in a session, or `dejima attach <island> <path>`"
	tipInvite   = "👥 Invite a teammate — press s → Team & invites (or `dejima token invite`)"
	tipHostTerm = "🖥 Open an uncontained host terminal with [/] (operator-only)"
)

// footerTips builds the rotating tip pool for the current state.
func (m tuiModel) footerTips() []string {
	tips := []string{tipAttach, tipInvite}
	if m.hostTerminalsAvailable() {
		tips = append(tips, tipHostTerm)
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
