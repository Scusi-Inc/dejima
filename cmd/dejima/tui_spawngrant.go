package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// spawnGrantEditor is the per-island "sub-agent budget" overlay (opened from the
// island action menu). It views, sets, and revokes the island's spawn grant —
// the cap on how many ephemeral sub-agents the island's agents may spawn — which
// until now was CLI-only (`dejima spawn grant/show/revoke`). Deny-all is the
// default: with no grant, the island's agents cannot spawn at all.
//
// The grant is fetched on open (GetSpawnGrant). "off" concurrency means no
// grant, so applying it revokes; any positive concurrency sets/updates the
// grant. The other caps bound a granted orchestrator so it can't spawn workers
// that collectively OOM the host (see the resource-vs-cap signals).
type spawnGrantEditor struct {
	island    string
	loading   bool
	loadErr   string
	granted   bool // current daemon state (a grant exists)
	used      int  // lifetime sub-agents spawned under the current grant (display)
	field     int  // focused row: 0 concurrent, 1 total, 2 ttl, 3 per-agent mem
	concSel   int
	totalSel  int
	ttlSel    int
	memSel    int
	busy      bool
	actionErr string
}

const (
	sgConcurrent = iota
	sgTotal
	sgTTL
	sgMem
	sgFieldCount
)

var (
	// spawnConcPresets — max live sub-agents at once. "off" (0) = no grant.
	spawnConcPresets = []struct {
		label string
		value int
	}{{"off — no spawning", 0}, {"1", 1}, {"2", 2}, {"4", 4}, {"8", 8}}
	// spawnTotalPresets — lifetime cap; 0 = unlimited within the grant.
	spawnTotalPresets = []struct {
		label string
		value int
	}{{"unlimited", 0}, {"5", 5}, {"10", 10}, {"25", 25}, {"50", 50}}
	// spawnTTLPresets — per-sub-agent max lifetime before reap.
	spawnTTLPresets = []struct{ label, value string }{{"no cap", ""}, {"30m", "30m"}, {"1h", "1h"}, {"4h", "4h"}}
	// spawnMemPresets — per-sub-agent memory cap.
	spawnMemPresets = []struct{ label, value string }{{"inherit default", ""}, {"256m", "256m"}, {"512m", "512m"}, {"1G", "1g"}}
)

// openSpawnGrantEditor opens the overlay and fetches the island's current grant.
func (m tuiModel) openSpawnGrantEditor(island string) (tea.Model, tea.Cmd) {
	if island == "" {
		return m, nil
	}
	// Default the form to a sensible starting grant (1 concurrent, otherwise
	// open) so applying from a clean slate creates a real grant rather than a
	// no-op. The load overwrites these when a grant already exists.
	m.spawnGrant = &spawnGrantEditor{island: island, loading: true, concSel: 1}
	return m, m.loadSpawnGrantCmd(island)
}

type spawnGrantLoadedMsg struct {
	island string
	resp   *api.SpawnGrantResponse
	err    error
}

func (m tuiModel) loadSpawnGrantCmd(island string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.GetSpawnGrant(ctx, island)
		return spawnGrantLoadedMsg{island: island, resp: resp, err: err}
	}
}

func (ed *spawnGrantEditor) applyLoaded(msg spawnGrantLoadedMsg) {
	ed.loading = false
	if msg.err != nil {
		ed.loadErr = msg.err.Error()
		return
	}
	ed.loadErr = ""
	if msg.resp == nil || !msg.resp.Granted || msg.resp.Grant == nil {
		ed.granted = false
		return
	}
	g := msg.resp.Grant
	ed.granted = true
	ed.used = g.Used
	ed.concSel = nearestIntPreset(g.MaxConcurrent, spawnConcPresets)
	ed.totalSel = nearestIntPreset(g.MaxTotal, spawnTotalPresets)
	ed.ttlSel = durationPresetIndex(g.TTL)
	ed.memSel = strPresetIndex(g.PerAgentMemory, spawnMemPresets)
}

// nearestIntPreset returns the index of the preset whose value equals v, else 0
// (a value set via the CLI outside the preset set falls back to the first).
func nearestIntPreset(v int, presets []struct {
	label string
	value int
}) int {
	for i, p := range presets {
		if p.value == v {
			return i
		}
	}
	return 0
}

func strPresetIndex(v string, presets []struct{ label, value string }) int {
	for i, p := range presets {
		if p.value == v {
			return i
		}
	}
	return 0
}

// durationPresetIndex maps a stored TTL to its preset row (0 = no cap).
func durationPresetIndex(d time.Duration) int {
	for i, p := range spawnTTLPresets {
		if p.value == "" {
			continue
		}
		if pd, err := time.ParseDuration(p.value); err == nil && pd == d {
			return i
		}
	}
	return 0
}

func (m tuiModel) spawnGrantKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := m.spawnGrant
	if ed.busy || ed.loading {
		if msg.String() == "esc" || msg.String() == "q" {
			m.spawnGrant = nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.spawnGrant = nil
		return m, nil
	case "down", "j":
		if ed.field < sgFieldCount-1 {
			ed.field++
		}
		return m, nil
	case "up", "k":
		if ed.field > 0 {
			ed.field--
		}
		return m, nil
	case "left", "h":
		ed.cycle(-1)
		return m, nil
	case "right", "l", " ":
		ed.cycle(1)
		return m, nil
	case "x", "d":
		// Revoke the grant (back to deny-all). No-op when nothing is granted.
		if !ed.granted {
			return m, nil
		}
		ed.busy = true
		ed.actionErr = ""
		return m, m.revokeSpawnGrantCmd(ed.island)
	case "enter":
		// "off" concurrency means no grant → apply revokes; otherwise set/update.
		if spawnConcPresets[ed.concSel].value == 0 {
			if !ed.granted {
				m.spawnGrant = nil // nothing to do: not granted and chose off
				return m, nil
			}
			ed.busy = true
			ed.actionErr = ""
			return m, m.revokeSpawnGrantCmd(ed.island)
		}
		ed.busy = true
		ed.actionErr = ""
		return m, m.applySpawnGrantCmd(ed.island, api.SpawnGrantRequest{
			MaxConcurrent:  spawnConcPresets[ed.concSel].value,
			MaxTotal:       spawnTotalPresets[ed.totalSel].value,
			TTL:            spawnTTLPresets[ed.ttlSel].value,
			PerAgentMemory: spawnMemPresets[ed.memSel].value,
		})
	}
	return m, nil
}

// cycle advances the focused field's preset by dir (wrapping).
func (ed *spawnGrantEditor) cycle(dir int) {
	wrap := func(sel, n int) int { return (sel + dir + n) % n }
	switch ed.field {
	case sgConcurrent:
		ed.concSel = wrap(ed.concSel, len(spawnConcPresets))
	case sgTotal:
		ed.totalSel = wrap(ed.totalSel, len(spawnTotalPresets))
	case sgTTL:
		ed.ttlSel = wrap(ed.ttlSel, len(spawnTTLPresets))
	case sgMem:
		ed.memSel = wrap(ed.memSel, len(spawnMemPresets))
	}
}

type spawnGrantMutatedMsg struct {
	island  string
	revoked bool
	err     error
}

func (m tuiModel) applySpawnGrantCmd(island string, req api.SpawnGrantRequest) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := c.SetSpawnGrant(ctx, island, req)
		return spawnGrantMutatedMsg{island: island, err: err}
	}
}

func (m tuiModel) revokeSpawnGrantCmd(island string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return spawnGrantMutatedMsg{island: island, revoked: true, err: c.RevokeSpawnGrant(ctx, island)}
	}
}

func (m tuiModel) renderSpawnGrantEditor() string {
	ed := m.spawnGrant
	var b strings.Builder
	b.WriteString(styleHeader.Render("Sub-agent budget — " + ed.island))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("how many ephemeral sub-agents this island's agents may spawn (deny-all by default)"))
	b.WriteString("\n\n")

	switch {
	case ed.loading:
		return b.String() + styleMuted.Render("loading…")
	case ed.loadErr != "":
		return b.String() + styleErrored.Render("error: "+ed.loadErr) +
			"\n\n" + styleMuted.Render("(is the daemon reachable? esc to close)")
	}

	state := styleMuted.Render("not granted — agents here cannot spawn")
	if ed.granted {
		used := fmt.Sprintf(" · used %d", ed.used)
		state = styleRunning.Render("granted") + styleMuted.Render(used)
	}
	b.WriteString("  " + state + "\n\n")

	rows := [sgFieldCount]struct{ label, val string }{
		{"Max concurrent", spawnConcPresets[ed.concSel].label},
		{"Max total", spawnTotalPresets[ed.totalSel].label},
		{"Per-agent TTL", spawnTTLPresets[ed.ttlSel].label},
		{"Per-agent memory", spawnMemPresets[ed.memSel].label},
	}
	for i, r := range rows {
		lead := "   "
		val := styleMuted.Render("‹ ") + r.val + styleMuted.Render(" ›")
		if i == ed.field {
			lead = styleAccent.Render(" ▸ ")
			val = styleSelected.Render(" ‹ " + r.val + " › ")
		}
		b.WriteString(fmt.Sprintf("%s%-18s %s\n", lead, r.label, val))
	}
	b.WriteString("\n")
	if ed.busy {
		return b.String() + styleAccent.Render("applying…")
	}
	if ed.actionErr != "" {
		b.WriteString(styleErrored.Render("✗ "+ed.actionErr) + "\n\n")
	}
	b.WriteString(styleMuted.Render("↑/↓ field · ←/→ change · ⏎ apply · "))
	if ed.granted {
		b.WriteString(styleMuted.Render("[x] revoke · "))
	}
	b.WriteString(styleMuted.Render("esc cancel"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("set Max concurrent to “off” and apply to revoke; types/CPUs via `dejima spawn grant`"))
	return b.String()
}
