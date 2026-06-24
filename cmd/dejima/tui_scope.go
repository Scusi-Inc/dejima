package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// scopeView is the full-pane Port scope-picker (opened with `P` on an island).
// It lists the island's brokered host-file grants (Port scopes), lets an
// operator add a scoped host path (:ro / :rw) or revoke one, and — crucially —
// makes the deny-all posture visible: an island with no scopes shows
// "deny-all: no host files reachable" (the same wording as `dejima port list`),
// so the security stance is obvious in the UI rather than implied by emptiness.
//
// Mutations are operator-gated by the API (POST/DELETE require capOperate); a
// viewer's grant/revoke comes back as a 403, surfaced in errText. The view does
// not duplicate that gate client-side — the daemon is the single source of
// truth — matching how the other mutating overlays (resources, model) behave.
type scopeView struct {
	island  string
	loading bool
	errText string // last load/mutation error, shown until the next action
	scopes  []api.PortScopeView
	sel     int  // selected scope row (for revoke)
	busy    bool // a grant/revoke is in flight

	// Add-a-scope sub-form. Toggled with `a`; the host path is typed, the mode
	// flipped with ←/→ (or `m`), submitted with ⏎.
	adding bool
	pathIn string
	modeRW bool // false = :ro (default), true = :rw

	// confirmRevoke names a scope pending a y/n revoke confirmation ("" = none).
	confirmRevoke string
}

func (m tuiModel) openScopeView(island string) (tea.Model, tea.Cmd) {
	if island == "" {
		return m, nil
	}
	m.scope = &scopeView{island: island, loading: true}
	return m, m.loadScopesCmd(island)
}

type scopesLoadedMsg struct {
	island string
	resp   *api.PortScopesResponse
	err    error
}

func (m tuiModel) loadScopesCmd(island string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.ListPortScopes(ctx, island)
		return scopesLoadedMsg{island: island, resp: resp, err: err}
	}
}

// scopeMutatedMsg is the result of a grant or revoke; the view reloads after.
type scopeMutatedMsg struct {
	island string
	verb   string // "grant" | "revoke"
	err    error
}

func (m tuiModel) grantScopeCmd(island, hostPath, mode string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := c.GrantPortScope(ctx, island, hostPath, mode)
		return scopeMutatedMsg{island: island, verb: "grant", err: err}
	}
}

func (m tuiModel) revokeScopeCmd(island, scope string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := c.RevokePortScope(ctx, island, scope)
		return scopeMutatedMsg{island: island, verb: "revoke", err: err}
	}
}

// applyLoaded fills the view from a list result, clamping the cursor.
func (v *scopeView) applyLoaded(msg scopesLoadedMsg) {
	v.loading = false
	if msg.err != nil {
		v.errText = msg.err.Error()
		return
	}
	v.errText = ""
	if msg.resp != nil {
		v.scopes = msg.resp.Scopes
	} else {
		v.scopes = nil
	}
	if v.sel >= len(v.scopes) {
		v.sel = max(0, len(v.scopes)-1)
	}
}

// scopeKey drives the scope picker. The picker owns every key while open. In the
// add sub-form, typing edits the host path and ←/→ flip the mode; otherwise j/k
// move between scope rows, `a` opens the add form, and `d`/`x` revoke (confirmed).
func (m tuiModel) scopeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.scope
	if v.busy {
		// Allow esc to close even mid-flight (the result still lands harmlessly).
		if msg.String() == "esc" {
			m.scope = nil
		}
		return m, nil
	}

	// A pending revoke confirmation captures y/n before anything else.
	if v.confirmRevoke != "" {
		switch msg.String() {
		case "y", "Y", "enter":
			scope := v.confirmRevoke
			v.confirmRevoke = ""
			v.busy = true
			v.errText = ""
			return m, m.revokeScopeCmd(v.island, scope)
		default:
			v.confirmRevoke = ""
			return m, nil
		}
	}

	if v.adding {
		switch msg.String() {
		case "esc":
			v.adding = false
			v.pathIn = ""
			return m, nil
		case "enter":
			path := strings.TrimSpace(v.pathIn)
			if path == "" {
				v.errText = "host path required"
				return m, nil
			}
			if !filepath.IsAbs(path) {
				v.errText = "host path must be absolute (it resolves on the daemon host)"
				return m, nil
			}
			mode := "ro"
			if v.modeRW {
				mode = "rw"
			}
			v.busy = true
			v.errText = ""
			v.adding = false
			return m, m.grantScopeCmd(v.island, path, mode)
		case "left", "right", "tab":
			v.modeRW = !v.modeRW
			return m, nil
		case "backspace":
			if len(v.pathIn) > 0 {
				v.pathIn = v.pathIn[:len(v.pathIn)-1]
			}
			return m, nil
		default:
			v.pathIn += printableInput(msg)
			return m, nil
		}
	}

	switch msg.String() {
	case "esc", "q", "P":
		m.scope = nil
		return m, nil
	case "r":
		v.loading = true
		v.errText = ""
		return m, m.loadScopesCmd(v.island)
	case "a", "+":
		v.adding = true
		v.pathIn = ""
		v.modeRW = false
		v.errText = ""
		return m, nil
	case "j", "down":
		if v.sel < len(v.scopes)-1 {
			v.sel++
		}
		return m, nil
	case "k", "up":
		if v.sel > 0 {
			v.sel--
		}
		return m, nil
	case "g", "home":
		v.sel = 0
		return m, nil
	case "G", "end":
		v.sel = max(0, len(v.scopes)-1)
		return m, nil
	case "d", "x", "delete":
		if len(v.scopes) > 0 {
			v.confirmRevoke = v.scopes[v.sel].Name
		}
		return m, nil
	}
	return m, nil
}

// renderScopeView renders the picker body; the caller wraps it in stylePane.
func (m tuiModel) renderScopeView() string {
	v := m.scope
	var b strings.Builder
	b.WriteString(styleTitle.Render("Port scopes — " + v.island))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("brokered host-file access · every crossing is audited in the Ledger"))
	b.WriteString("\n\n")

	if v.loading {
		b.WriteString(styleMuted.Render("loading…"))
		return b.String()
	}

	// The deny-all banner is the security headline: with no scopes, NOTHING on
	// the host is reachable. Mirror the CLI's wording so the posture reads the
	// same everywhere.
	if len(v.scopes) == 0 {
		b.WriteString(styleRunning.Render("deny-all: no host files reachable"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("this island has no Port scopes — grant one to open a scoped, audited window."))
		b.WriteString("\n\n")
	} else {
		hdr := fmt.Sprintf("   %-14s  %-5s  %-19s  %s", "SCOPE", "MODE", "GRANTED", "HOST PATH")
		b.WriteString(styleHeader.Render(hdr))
		b.WriteString("\n")
		for i, sc := range v.scopes {
			lead := "   "
			row := fmt.Sprintf("%-14s  %-5s  %-19s  %s",
				truncate(sc.Name, 14),
				sc.Mode,
				sc.GrantedAt.Local().Format("2006-01-02 15:04:05"),
				sc.HostPath)
			if i == v.sel && !v.adding {
				lead = styleAccent.Render(" ▸ ")
				row = styleSelected.Render(row)
			}
			b.WriteString(lead + row + "\n")
		}
		b.WriteString("\n")
	}

	// A pending revoke confirmation sits above the form/footer.
	if v.confirmRevoke != "" {
		b.WriteString(styleErrored.Render("revoke scope " + v.confirmRevoke + "?  [y] yes   [any] no"))
		b.WriteString("\n\n")
	}

	if v.adding {
		mode := styleMuted.Render(" ro ") + " " + styleSelected.Render(" rw ")
		if !v.modeRW {
			mode = styleSelected.Render(" ro ") + " " + styleMuted.Render(" rw ")
		}
		b.WriteString(styleHeader.Render("Grant a host directory"))
		b.WriteString("\n")
		b.WriteString("  host path: " + styleAccent.Render(v.pathIn+"_"))
		b.WriteString("\n")
		b.WriteString("  mode:      " + mode + styleMuted.Render("   (←/→ to flip; rw also allows port write back)"))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("type an absolute host path · ←/→ mode · ⏎ grant · esc cancel"))
		if v.errText != "" {
			b.WriteString("\n" + styleErrored.Render(v.errText))
		}
		return b.String()
	}

	if v.busy {
		b.WriteString(styleAccent.Render("applying…"))
		return b.String()
	}

	if v.errText != "" {
		b.WriteString(styleErrored.Render("error: " + v.errText))
		b.WriteString("\n\n")
	}

	b.WriteString(styleMuted.Render("a add · d/x revoke · j/k select · r refresh · esc close"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("grants need an operator token; a viewer's add/revoke is refused by the daemon."))
	return b.String()
}
