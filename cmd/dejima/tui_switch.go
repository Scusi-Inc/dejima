package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/clientcfg"
)

// switcherModel drives the connection-target overlay: pick a saved profile
// (local socket or a remote daemon), add one, or delete one. Activating a
// profile rebuilds the API client and hot-swaps it into the running TUI.
type switcherModel struct {
	profiles []clientcfg.Profile // index 0 is always synthetic "local"
	cursor   int
	step     switcherStep
	label    string // add-flow inputs
	host     string
	err      string
}

type switcherStep int

const (
	swList switcherStep = iota
	swAddLabel
	swAddHost
)

// openSwitcher loads saved profiles (prepending a synthetic "local") and opens
// the overlay with the cursor on the currently-active target.
func (m tuiModel) openSwitcher() (tea.Model, tea.Cmd) {
	cfg, _ := clientcfg.Load()
	profiles := append([]clientcfg.Profile{{Name: "local", Host: ""}}, cfg.Profiles...)
	s := &switcherModel{profiles: profiles}
	for i, p := range profiles {
		if p.Host == m.activeHost {
			s.cursor = i
			break
		}
	}
	m.switcher = s
	return m, nil
}

func (m tuiModel) switcherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.switcher
	switch s.step {
	case swAddLabel:
		return m.switcherAddLabelKey(msg)
	case swAddHost:
		return m.switcherAddHostKey(msg)
	}
	// swList
	switch msg.String() {
	case "esc", "q", "s", "ctrl+c":
		m.switcher = nil
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.profiles)-1 {
			s.cursor++
		}
	case "a":
		s.step, s.label, s.host, s.err = swAddLabel, "", "", ""
	case "d":
		if s.cursor > 0 { // never delete the synthetic "local"
			return m.switcherDelete()
		}
	case "enter":
		return m.switcherActivate()
	}
	return m, nil
}

// switcherActivate rebuilds the client for the selected profile and swaps it in,
// clearing stale island data and kicking a fresh fetch.
func (m tuiModel) switcherActivate() (tea.Model, tea.Cmd) {
	s := m.switcher
	p := s.profiles[s.cursor]
	c, err := clientForHost(p.Host)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}
	// Persist the active choice (label only meaningful for real profiles).
	cfg, _ := clientcfg.Load()
	cfg.ActiveProfile = p.Name
	_ = clientcfg.Save(cfg)

	m.client = c
	m.activeHost = p.Host
	m.activeLabel = p.Name
	m.islands = nil
	m.detail = nil
	m.overview = nil
	m.events_ = nil
	m.selected = 0
	m.lastError = ""
	m.switcher = nil
	return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
}

func (m tuiModel) switcherDelete() (tea.Model, tea.Cmd) {
	s := m.switcher
	target := s.profiles[s.cursor]
	cfg, _ := clientcfg.Load()
	kept := cfg.Profiles[:0]
	for _, p := range cfg.Profiles {
		if p.Name != target.Name || p.Host != target.Host {
			kept = append(kept, p)
		}
	}
	cfg.Profiles = kept
	_ = clientcfg.Save(cfg)
	// Rebuild the displayed list (keep synthetic local at 0).
	s.profiles = append([]clientcfg.Profile{{Name: "local", Host: ""}}, kept...)
	if s.cursor >= len(s.profiles) {
		s.cursor = len(s.profiles) - 1
	}
	return m, nil
}

func (m tuiModel) switcherAddLabelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.switcher
	switch msg.String() {
	case "esc":
		s.step = swList
	case "enter":
		if strings.TrimSpace(s.label) == "" {
			return m, nil
		}
		s.step = swAddHost
	case "backspace":
		if s.label != "" {
			s.label = s.label[:len(s.label)-1]
		}
	default:
		if len(msg.String()) == 1 {
			s.label += msg.String()
		}
	}
	return m, nil
}

func (m tuiModel) switcherAddHostKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.switcher
	switch msg.String() {
	case "esc":
		s.step = swAddLabel
	case "enter":
		host := strings.TrimSpace(s.host)
		if host == "" {
			s.err = "host is required (e.g. minion.tailnet:7273)"
			return m, nil
		}
		cfg, _ := clientcfg.Load()
		cfg.Profiles = append(cfg.Profiles, clientcfg.Profile{Name: strings.TrimSpace(s.label), Host: host})
		_ = clientcfg.Save(cfg)
		s.profiles = append([]clientcfg.Profile{{Name: "local", Host: ""}}, cfg.Profiles...)
		s.cursor = len(s.profiles) - 1
		s.step, s.err = swList, ""
	case "backspace":
		if s.host != "" {
			s.host = s.host[:len(s.host)-1]
		}
	default:
		if len(msg.String()) == 1 {
			s.host += msg.String()
		}
	}
	return m, nil
}

func (s *switcherModel) view() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Connection"))
	b.WriteString("\n\n")

	switch s.step {
	case swAddLabel:
		b.WriteString(styleMuted.Render("Name this connection (e.g. minion, work-vps)."))
		b.WriteString("\n\nname: " + styleAccent.Render(s.label+"_"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] next   [esc] back"))
	case swAddHost:
		b.WriteString(styleMuted.Render("Daemon address, host:port (the DEJIMA_HOST value)."))
		b.WriteString("\n\nhost: " + styleAccent.Render(s.host+"_"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] save   [esc] back"))
	default:
		b.WriteString(styleMuted.Render("Pick a target. Switching reconnects in place."))
		b.WriteString("\n\n")
		for i, p := range s.profiles {
			target := p.Host
			if target == "" {
				target = "unix socket"
			}
			row := fmt.Sprintf("%-14s %s", truncate(p.Name, 14), styleMuted.Render(target))
			if i == s.cursor {
				b.WriteString(styleSelected.Render("▶ " + row))
			} else {
				b.WriteString("  " + row)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] connect   [a] add   [d] delete   [esc] close"))
	}

	if s.err != "" {
		b.WriteString("\n\n" + styleErrored.Render("✗ "+s.err))
	}
	return b.String()
}
