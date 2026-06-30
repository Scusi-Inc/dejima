package main

import (
	"fmt"
	"net"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/invite"
)

// normalizeHost trims the entered daemon address and appends the default port
// (7273) when the operator gave a bare host with no :port — the #1 connection
// mistake, since a port-less host otherwise dials :80 and is refused.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		return net.JoinHostPort(host, "7273")
	}
	return host
}

// switcherModel drives the connection-target overlay: pick a saved profile
// (local socket or a remote daemon), add one, or delete one. Activating a
// profile rebuilds the API client and hot-swaps it into the running TUI.
type switcherModel struct {
	profiles []clientcfg.Profile // index 0 is always synthetic "local"
	cursor   int
	step     switcherStep
	label    string // add-flow inputs
	host     string
	blob     string // join-flow input: a pasted `dejima-invite:` blob
	err      string
}

type switcherStep int

const (
	swList switcherStep = iota
	swAddLabel
	swAddHost
	swJoin // paste a team invite blob → decode → save profile → connect
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
	case swJoin:
		return m.switcherJoinKey(msg)
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
	case "J", "i":
		// Join via a team invite — paste the blob an owner sent you. (Capital J and
		// `i` both open it; lowercase j is list navigation above.)
		s.step, s.blob, s.err = swJoin, "", ""
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
	// An explicit pick in the switcher is profile- or local-sourced, never env —
	// even if DEJIMA_HOST is set, the user just chose a target by hand.
	if p.Host == "" {
		m.activeSource = "local"
	} else {
		m.activeSource = "profile"
	}
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
	// Shared store mutation — the same path as `dejima profile rm`. Removes the
	// profile and clears ActiveProfile if it was the one deleted (profile names
	// are unique, so a name match is exact).
	_ = clientcfg.RemoveProfile(target.Name)

	// If we just deleted the profile we're connected through, the live client
	// still points at the now-gone host — and that host may be exactly why the
	// user is deleting it (e.g. a corrupt entry that won't dial). Fall back to
	// the local socket so deleting a broken profile actually recovers the
	// session instead of leaving it wedged on a dead connection.
	var cmd tea.Cmd
	if target.Host == m.activeHost {
		if c, err := clientForHost(""); err == nil {
			m.client = c
			m.activeHost = ""
			m.activeLabel = "local"
			m.activeSource = "local"
			m.islands = nil
			m.detail = nil
			m.overview = nil
			m.events_ = nil
			m.selected = 0
			m.lastError = ""
			cmd = tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
		}
	}

	// Rebuild the displayed list from the persisted config (keep synthetic local at 0).
	cfg, _ := clientcfg.Load()
	s.profiles = append([]clientcfg.Profile{{Name: "local", Host: ""}}, cfg.Profiles...)
	if s.cursor >= len(s.profiles) {
		s.cursor = len(s.profiles) - 1
	}
	return m, cmd
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
		s.label += printableInput(msg)
	}
	return m, nil
}

func (m tuiModel) switcherAddHostKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.switcher
	switch msg.String() {
	case "esc":
		s.step = swAddLabel
	case "enter":
		host := normalizeHost(s.host)
		if host == "" {
			s.err = "host is required (e.g. 100.77.85.107:7273)"
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
		s.host += printableInput(msg)
	}
	return m, nil
}

// switcherJoinKey drives the join-via-invite step: collect a pasted
// `dejima-invite:` blob, then decode + save + connect on Enter. Unlike the
// label/host fields, this accepts a multi-rune paste (a bracketed paste arrives
// as a single KeyRunes event carrying the whole blob), since nobody hand-types
// a ~150-char invite.
func (m tuiModel) switcherJoinKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.switcher
	switch msg.String() {
	case "esc":
		s.step, s.err = swList, ""
		return m, nil
	case "enter":
		return m.switcherJoinSubmit()
	case "backspace":
		if r := []rune(s.blob); len(r) > 0 {
			s.blob = string(r[:len(r)-1])
		}
		return m, nil
	}
	// Accept typed and pasted runes (the blob is the whole Runes slice).
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		s.blob += string(msg.Runes)
	}
	return m, nil
}

// switcherJoinSubmit decodes the pasted invite, persists it as the active
// profile (host + token), and hot-swaps the client onto it — the TUI equivalent
// of `dejima join <blob>`. Decode/save errors render verbatim in the overlay.
func (m tuiModel) switcherJoinSubmit() (tea.Model, tea.Cmd) {
	s := m.switcher
	p, err := invite.Decode(s.blob)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}
	name, err := clientcfg.SaveInvite(p)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}
	// SaveInvite made the profile active and stored its token, so clientForHost
	// resolves the bearer automatically (tokenForHost: profile token, env-wins).
	c, err := clientForHost(p.Host)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}
	m.client = c
	m.activeHost = p.Host
	m.activeLabel = name
	m.activeSource = "profile"
	m.islands = nil
	m.detail = nil
	m.overview = nil
	m.events_ = nil
	m.selected = 0
	m.lastError = ""
	m.lastNotice = fmt.Sprintf("joined %s as %s — saved profile %q", p.Host, p.Role, name)
	m.switcher = nil
	return m, tea.Batch(m.fetchListCmd(), m.fetchOverviewCmd())
}

// printableInput returns the text a key event should contribute to a text field,
// or "" for anything that isn't a single printable character. Filtering here
// keeps control characters (NUL, etc.) and multi-rune editing keys out of saved
// labels and hosts — a stray control byte in a host string later splices into a
// request URL and fails to parse (see clientForHost).
func printableInput(msg tea.KeyMsg) string {
	// Both KeyRunes and KeySpace carry the typed rune in Runes; editing/control
	// keys (enter, backspace, ctrl+*, arrows) carry none. Keying off Runes — not
	// Type — accepts space while rejecting control characters and pastes.
	if len(msg.Runes) != 1 || !unicode.IsPrint(msg.Runes[0]) {
		return ""
	}
	return string(msg.Runes[0])
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
		b.WriteString(styleMuted.Render("Daemon address as ") + styleAccent.Render("host:port") + styleMuted.Render(" — the port is required (default ") + styleAccent.Render("7273") + styleMuted.Render(")."))
		b.WriteString("\n" + styleMuted.Render("  e.g.  ") + styleAccent.Render("100.77.85.107:7273") + styleMuted.Render("  (a Tailscale IP)    or    ") + styleAccent.Render("minion:7273"))
		b.WriteString("\n\nhost: " + styleAccent.Render(s.host+"_"))
		b.WriteString("\n\n" + styleMuted.Render("How to find it: on the machine running dejimad, run ") + styleAccent.Render("tailscale ip") + styleMuted.Render(" → use that address with ") + styleAccent.Render(":7273") + styleMuted.Render("."))
		b.WriteString("\n" + styleMuted.Render("(a bare host with no :port gets :7273 added automatically.)"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] save   [esc] back"))
	case swJoin:
		b.WriteString(styleMuted.Render("Joining a teammate's server? Paste the ") + styleAccent.Render("dejima-invite:") + styleMuted.Render(" blob they sent you."))
		b.WriteString("\n" + styleMuted.Render("It carries the host + your access token — no env vars, no manual host:port."))
		// Show a length indicator rather than the raw secret-bearing blob.
		preview := styleMuted.Render("(nothing pasted yet)")
		if s.blob != "" {
			preview = styleAccent.Render(fmt.Sprintf("✓ %d characters pasted", len([]rune(strings.TrimSpace(s.blob)))))
		}
		b.WriteString("\n\ninvite: " + preview)
		b.WriteString("\n\n" + styleMuted.Render("[⏎] join & connect   [esc] back"))
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
		b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] connect   [a] add   [J] join via invite   [d] delete   [esc] close"))
	}

	if s.err != "" {
		b.WriteString("\n\n" + styleErrored.Render("✗ "+s.err))
	}
	return b.String()
}
