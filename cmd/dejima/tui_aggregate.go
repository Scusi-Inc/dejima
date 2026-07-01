package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// aggregateView is the host-utilization panel (opened with `%`): a
// privacy-preserving, host-wide rollup — counts + total mem/cpu/disk across ALL
// islands, with NO names/repos/owners. It's the "see the shared load, not what's
// running" surface for a multi-tenant daemon, readable by any authenticated
// caller. Data comes from GET /v1/aggregate (api.Client.Aggregate); the server
// handler is a1's P3, so until that ships the fetch 404s and the panel says so.
type aggregateView struct {
	loading bool
	loadErr string
	resp    *api.AggregateResponse
}

func (m tuiModel) openAggregateView() (tea.Model, tea.Cmd) {
	m.aggregate = &aggregateView{loading: true}
	return m, m.loadAggregateCmd()
}

type aggregateLoadedMsg struct {
	resp *api.AggregateResponse
	err  error
}

func (m tuiModel) loadAggregateCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.Aggregate(ctx)
		return aggregateLoadedMsg{resp: resp, err: err}
	}
}

func (v *aggregateView) applyLoaded(msg aggregateLoadedMsg) {
	v.loading = false
	if msg.err != nil {
		v.loadErr = msg.err.Error()
		return
	}
	v.loadErr = ""
	v.resp = msg.resp
}

func (m tuiModel) aggregateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "%", "ctrl+c":
		m.aggregate = nil
		return m, nil
	case "r":
		m.aggregate.loading = true
		m.aggregate.loadErr = ""
		return m, m.loadAggregateCmd()
	}
	return m, nil
}

func (m tuiModel) renderAggregateView() string {
	v := m.aggregate
	var b strings.Builder
	b.WriteString(styleHeader.Render("Host utilization"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("shared-host totals across all islands — no names, repos, or owners"))
	b.WriteString("\n\n")

	switch {
	case v.loading:
		return b.String() + styleMuted.Render("loading…")
	case v.loadErr != "":
		// The most likely cause pre-P3 is "no such route" — say what's missing
		// rather than dumping a raw 404.
		return b.String() +
			styleWaiting.Render("host aggregate unavailable") + "\n" +
			styleMuted.Render("  "+v.loadErr) + "\n\n" +
			styleMuted.Render("  needs a daemon exposing GET /v1/aggregate. [r] retry · [esc] close")
	case v.resp == nil:
		return b.String() + styleMuted.Render("no data")
	}

	r := v.resp
	row := func(label, val string) {
		b.WriteString(fmt.Sprintf("  %-16s %s\n", styleMuted.Render(label), styleAccent.Render(val)))
	}
	row("Islands", fmt.Sprintf("%d", r.TotalIslands))
	row("Running", fmt.Sprintf("%d", r.Running))
	row("Hibernated", fmt.Sprintf("%d", r.Hibernated))
	b.WriteString("\n")

	mem := humanBytes(r.MemoryUsageBytes)
	if r.MemoryLimitBytes > 0 {
		pct := float64(r.MemoryUsageBytes) / float64(r.MemoryLimitBytes) * 100
		mem = fmt.Sprintf("%s / %s  (%.0f%%)", humanBytes(r.MemoryUsageBytes), humanBytes(r.MemoryLimitBytes), pct)
	}
	row("Memory", mem)
	row("CPU", fmt.Sprintf("%.1f%%", r.CPUPercent))
	if r.DiskTotalBytes > 0 {
		row("Disk", humanBytes(uint64(r.DiskTotalBytes)))
	}
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("[r] refresh · [esc] close"))
	return b.String()
}
