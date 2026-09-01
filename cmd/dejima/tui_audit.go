package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/ledger"
)

// auditView is the full-pane audit-log viewer (opened with `A`). It reads the
// tail of the hash-chained ledger and shows its chain-verification status, so an
// operator can eyeball recent governance activity and spot tampering without
// leaving the dashboard. Read-only — export/filtering live in `dejima audit`.
type auditView struct {
	loading  bool
	loadErr  string
	entries  []ledger.Entry // newest first
	total    int
	returned int
	verified bool
	verErr   string
	scroll   int
}

// auditTail caps how many recent entries the pane pulls (the CLI reads the full
// log); enough to scroll through a session's worth without a huge transfer.
const auditTail = 500

func (m tuiModel) openAuditView() (tea.Model, tea.Cmd) {
	m.audit = &auditView{loading: true}
	return m, m.loadAuditCmd()
}

type auditLoadedMsg struct {
	resp *api.AuditResponse
	err  error
}

func (m tuiModel) loadAuditCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.Audit(ctx, api.AuditQuery{Limit: auditTail})
		return auditLoadedMsg{resp: resp, err: err}
	}
}

// applyLoaded fills the view from a fetch result, reversing to newest-first.
func (v *auditView) applyLoaded(msg auditLoadedMsg) {
	v.loading = false
	if msg.err != nil {
		v.loadErr = msg.err.Error()
		return
	}
	v.loadErr = ""
	v.total = msg.resp.Total
	v.returned = msg.resp.Returned
	v.verified = msg.resp.Verified
	v.verErr = msg.resp.Error
	v.entries = v.entries[:0]
	for i := len(msg.resp.Entries) - 1; i >= 0; i-- {
		v.entries = append(v.entries, msg.resp.Entries[i])
	}
	if v.scroll > len(v.entries)-1 {
		v.scroll = 0
	}
}

// auditKey drives the audit pane: esc/q closes, r refreshes, j/k/arrows and
// page keys scroll. The pane owns every key while open.
func (m tuiModel) auditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.audit
	switch msg.String() {
	case "esc", "q", "A":
		m.audit = nil
		return m, nil
	case "r":
		v.loading = true
		v.loadErr = ""
		return m, m.loadAuditCmd()
	case "j", "down":
		v.scroll = clampAuditScroll(v.scroll+1, len(v.entries))
	case "k", "up":
		v.scroll = clampAuditScroll(v.scroll-1, len(v.entries))
	case "g", "home":
		v.scroll = 0
	case "G", "end":
		v.scroll = clampAuditScroll(len(v.entries), len(v.entries))
	case "pgdown", "ctrl+d":
		v.scroll = clampAuditScroll(v.scroll+10, len(v.entries))
	case "pgup", "ctrl+u":
		v.scroll = clampAuditScroll(v.scroll-10, len(v.entries))
	}
	return m, nil
}

func clampAuditScroll(s, n int) int {
	if s < 0 || n == 0 {
		return 0
	}
	if s > n-1 {
		return n - 1
	}
	return s
}

// renderAuditView renders the pane body to fit the available width/height. The
// caller (View) wraps the result in stylePane.
func (m tuiModel) renderAuditView() string {
	v := m.audit
	var b strings.Builder
	b.WriteString(styleTitle.Render("Audit ledger"))
	b.WriteString("\n")

	switch {
	case v.loading:
		b.WriteString(styleMuted.Render("loading…"))
		return b.String()
	case v.loadErr != "":
		b.WriteString(styleErrored.Render("error: " + v.loadErr))
		b.WriteString("\n\n" + styleMuted.Render("(is the daemon reachable? press r to retry, esc to close)"))
		return b.String()
	}

	// Chain-verification banner — the whole point of the pane.
	if v.verified {
		b.WriteString(styleRunning.Render(fmt.Sprintf("✓ hash chain intact — %d entries", v.total)))
		// ...qualified the moment a row on screen is one Dejima cannot vouch for.
		// The banner is true of the CHAIN and says nothing about whether a row was
		// true when written; read together without this line, the chain's assurance
		// gets attached to the row's claim.
		if note := chainNote(v.entries); note != "" {
			b.WriteString("\n" + styleMuted.Render("  " + note))
		}
	} else {
		b.WriteString(styleErrored.Render("⚠ CHAIN TAMPERED — " + v.verErr))
	}
	b.WriteString("\n\n")

	if len(v.entries) == 0 {
		b.WriteString(styleMuted.Render("ledger is empty (no operations recorded)"))
		b.WriteString("\n\n" + m.auditFooterHint())
		return b.String()
	}

	// Reserve lines for the title, banner, blanks, and footer hint.
	const chrome = 9 // title, banner (+ its qualifier), blanks, provenance legend, footer hint
	visible := m.height - chrome
	if visible < 3 {
		visible = 3
	}

	// The provenance column carries a mark only for rows Dejima cannot vouch for,
	// so the ordinary all-brokered ledger looks exactly as it did — the marks are
	// here to be rare, and a column that always says the same thing stops being
	// read. It is FIRST, before SEQ, because it qualifies everything after it.
	hdr := fmt.Sprintf("%-2s %-5s  %-19s  %-22s  %-14s  %-12s  %s",
		"", "SEQ", "WHEN", "TYPE", "ISLAND", "ACTOR", "DETAIL")
	b.WriteString(styleHeader.Render(hdr))
	b.WriteString("\n")

	end := v.scroll + visible
	if end > len(v.entries) {
		end = len(v.entries)
	}
	for _, e := range v.entries[v.scroll:end] {
		row := fmt.Sprintf("%-2s %-5d  %-19s  %-22s  %-14s  %-12s  %s",
			provenanceMark(e.Provenance),
			e.Seq,
			e.Time.Local().Format("2006-01-02 15:04:05"),
			truncate(e.Type, 22),
			truncate(e.Island, 14),
			truncate(e.Actor, 12),
			truncate(auditDetail(e), max(10, m.width-93)))
		line := row
		switch {
		case e.Decision == "denied":
			line = styleErrored.Render(row)
		case e.Provenance == ledger.ProvenanceSelfReported:
			// Dimmed rather than alarmed: a self-reported row is not a failure, it is
			// a weaker claim, and colouring it like a denial would teach people to
			// dismiss it. The mark says what it is; the dimming says how much of the
			// pane's assurance reaches it.
			line = styleMuted.Render(row)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if note := provenanceNote(v.entries); note != "" {
		b.WriteString(styleMuted.Render(note))
		b.WriteString("\n")
	}
	b.WriteString(m.auditFooterHint())
	return b.String()
}

func (m tuiModel) auditFooterHint() string {
	v := m.audit
	pos := ""
	if len(v.entries) > 0 {
		pos = fmt.Sprintf("  ·  row %d/%d", v.scroll+1, len(v.entries))
	}
	if v.returned < v.total {
		pos += fmt.Sprintf("  ·  showing last %d of %d", v.returned, v.total)
	}
	return styleMuted.Render("j/k scroll · r refresh · esc close" + pos)
}
