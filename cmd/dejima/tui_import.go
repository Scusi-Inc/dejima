package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// The TUI face of `dejima port intake`. Deliberately the BROKERED path, not
// `cp`: everything here is scoped to a granted Port scope and writes one Ledger
// entry per file. The unaudited convenience path stays on the CLI, where the
// help text can say so — a menu item labelled "Import files…" that quietly wrote
// no audit record would be exactly the divergence this feature exists to close.

type importStep int

const (
	importPickScope importStep = iota // choose one of the island's granted scopes
	importTypePath                    // type a path relative to that scope
	importRunning                     // request in flight
	importDone                        // results, including partial ones
	importCaps                        // raise the caps after a refusal, then retry
)

type importView struct {
	island string
	step   importStep

	loading bool
	err     string

	scopes []api.PortScopeView
	cursor int

	path      string
	recursive bool

	// Cap overrides for the NEXT attempt. Zero means "the daemon's default",
	// which is also what the API treats zero as — the numbers live on the daemon
	// because it is the side that can see the tree, and a copy here would be one
	// more thing to drift.
	maxFiles    string
	maxBytes    string
	capField    int // 0 = files, 1 = bytes
	capParseErr string

	result *api.PortIntakeResponse
}

type importScopesMsg struct {
	island string
	scopes []api.PortScopeView
	err    error
}

type importDoneMsg struct {
	res *api.PortIntakeResponse
	err error
}

func (m tuiModel) openImportView(island string) (tea.Model, tea.Cmd) {
	if island == "" {
		m.lastNotice = "select an island first — imports are scoped per-island"
		return m, nil
	}
	m.importPane = &importView{island: island, loading: true, recursive: true}
	return m, m.loadImportScopesCmd(island)
}

func (m tuiModel) loadImportScopesCmd(island string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := c.ListPortScopes(ctx, island)
		if err != nil {
			return importScopesMsg{island: island, err: err}
		}
		return importScopesMsg{island: island, scopes: out.Scopes}
	}
}

// runImportCmd performs the import. The timeout is generous because a recursive
// import is one brokered crossing per file and a large tree legitimately takes a
// while — a short timeout would abort halfway and leave exactly the partial
// state this surface then has to explain.
func (m tuiModel) runImportCmd(island, scope, path string, recursive bool, caps api.PortIntakeCaps) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err := c.PortIntakeRecursive(ctx, island, scope, path, "", recursive, caps)
		return importDoneMsg{res: res, err: err}
	}
}

// caps resolves the typed cap fields. An unparseable size is reported and the
// import does NOT run: sending 0 instead would re-request the default caps and
// come back with the identical refusal, which reads as "the override does not
// work" rather than "you typed the size wrong".
func (v *importView) caps() (api.PortIntakeCaps, error) {
	var out api.PortIntakeCaps
	if f := strings.TrimSpace(v.maxFiles); f != "" {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return out, fmt.Errorf("max files: %q is not a whole number", f)
		}
		out.MaxFiles = n
	}
	if b := strings.TrimSpace(v.maxBytes); b != "" {
		n, err := parseSize(b)
		if err != nil {
			return out, fmt.Errorf("max size: %w", err)
		}
		out.MaxBytes = n
	}
	return out, nil
}

// startImport is the single place an import is launched from, so the path that
// retries with raised caps cannot drift from the path that runs the first time.
func (m tuiModel) startImport(v *importView) (tea.Model, tea.Cmd) {
	c, err := v.caps()
	if err != nil {
		v.capParseErr = err.Error()
		v.step = importCaps
		return m, nil
	}
	v.capParseErr, v.err = "", ""
	v.step = importRunning
	return m, m.runImportCmd(v.island, v.scopes[v.cursor].Name, v.path, v.recursive, c)
}

func (m tuiModel) importKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.importPane
	switch v.step {
	case importPickScope:
		switch msg.String() {
		case "esc", "ctrl+[", "q":
			m.importPane = nil
		case "up", "k":
			if v.cursor > 0 {
				v.cursor--
			}
		case "down", "j":
			if v.cursor < len(v.scopes)-1 {
				v.cursor++
			}
		case "enter":
			if len(v.scopes) == 0 {
				return m, nil
			}
			v.step, v.path, v.err = importTypePath, "", ""
		}
		return m, nil

	case importTypePath:
		switch msg.String() {
		case "esc", "ctrl+[":
			v.step = importPickScope
		case "tab":
			// Toggling recursion is the difference between one file and a tree, so
			// it is a visible, deliberate keystroke rather than something inferred
			// from whether the path happens to name a directory.
			v.recursive = !v.recursive
		case "enter":
			if strings.TrimSpace(v.path) == "" {
				v.err = "type a path relative to the scope (\".\" for the whole scope)"
				return m, nil
			}
			return m.startImport(v)
		case "backspace":
			if v.path != "" {
				v.path = v.path[:len(v.path)-1]
			}
		default:
			if len(msg.String()) == 1 {
				v.path += msg.String()
			}
		}
		return m, nil

	case importRunning:
		return m, nil // input ignored while the request is in flight

	case importCaps:
		// Arrow/tab navigation only. Digits and the size suffixes are the whole
		// alphabet of both fields, so a j/k cursor would eat "1G" and a q would
		// close the pane mid-number.
		switch msg.String() {
		case "esc", "ctrl+[":
			v.step, v.capParseErr = importTypePath, ""
		case "tab", "up", "down":
			v.capField = 1 - v.capField
		case "enter":
			return m.startImport(v)
		case "backspace":
			if v.capField == 0 && v.maxFiles != "" {
				v.maxFiles = v.maxFiles[:len(v.maxFiles)-1]
			} else if v.capField == 1 && v.maxBytes != "" {
				v.maxBytes = v.maxBytes[:len(v.maxBytes)-1]
			}
		default:
			if t := pastableInput(msg); t != "" {
				if v.capField == 0 {
					v.maxFiles += t
				} else {
					v.maxBytes += t
				}
			}
		}
		return m, nil

	default: // importDone
		switch msg.String() {
		case "esc", "ctrl+[", "q", "enter":
			m.importPane = nil
		case "r":
			v.step, v.result, v.err = importPickScope, nil, ""
		case "c":
			// Reachable from a refusal so the operator never has to leave the TUI
			// for the CLI to raise a cap. The refusal above states the tree's real
			// size on both axes; these fields are where that number goes.
			v.step, v.capParseErr = importCaps, ""
		}
		return m, nil
	}
}

func (m tuiModel) onImportScopes(msg importScopesMsg) tuiModel {
	v := m.importPane
	if v == nil || v.island != msg.island {
		return m
	}
	v.loading = false
	if msg.err != nil {
		v.err = msg.err.Error()
		return m
	}
	v.scopes = msg.scopes
	return m
}

func (m tuiModel) onImportDone(msg importDoneMsg) tuiModel {
	v := m.importPane
	if v == nil {
		return m
	}
	v.step = importDone
	if msg.err != nil {
		v.err = msg.err.Error()
		return m
	}
	v.result = msg.res
	return m
}

func (m tuiModel) renderImport() string {
	v := m.importPane
	var b strings.Builder
	b.WriteString(styleHeader.Render("Import files → " + v.island))
	b.WriteString("\n\n")

	switch {
	case v.loading:
		b.WriteString(styleAccent.Render("⏳ loading this island's Port scopes…"))
		return b.String()
	case v.step == importPickScope && len(v.scopes) == 0:
		// Deny-all is the default, so "no scopes" is the ordinary first-run state
		// rather than an error. Say what to do about it.
		b.WriteString(styleMuted.Render(
			"This island has no Port scopes, so there is nothing it may read.\n\n" +
				"Grant one first — access is deny-all by default:\n"))
		b.WriteString("  dejima port grant " + v.island + " <host-path>:ro\n")
		b.WriteString("\n" + styleMuted.Render("[esc] close"))
		return b.String()
	}

	switch v.step {
	case importPickScope:
		b.WriteString(styleMuted.Render("Which granted scope?") + "\n\n")
		for i, s := range v.scopes {
			line := fmt.Sprintf("%-16s %s  (%s)", truncate(s.Name, 16), tildeify(s.HostPath), s.Mode)
			mark := "   "
			if i == v.cursor {
				mark = styleAccent.Render(" ▸ ")
			}
			b.WriteString(mark + line + "\n")
		}
		b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] choose   [esc] close"))

	case importTypePath:
		sc := v.scopes[v.cursor]
		b.WriteString(styleMuted.Render("Scope "+sc.Name+" — "+tildeify(sc.HostPath)) + "\n\n")
		b.WriteString("path within the scope: " + styleAccent.Render(v.path+"_") + "\n")
		mode := "one file"
		if v.recursive {
			mode = "a folder, one ledgered crossing per file"
		}
		b.WriteString("\n" + styleMuted.Render("importing: ") + mode + "\n")
		b.WriteString(styleMuted.Render("symlinks are never followed; skipped entries are reported") + "\n")
		if v.err != "" {
			b.WriteString("\n" + styleErrored.Render(v.err) + "\n")
		}
		if strings.TrimSpace(v.maxFiles) != "" || strings.TrimSpace(v.maxBytes) != "" {
			b.WriteString(styleMuted.Render(fmt.Sprintf("caps raised for this import: %s files, %s",
				capOrDefault(v.maxFiles), capOrDefault(v.maxBytes))) + "\n")
		}
		b.WriteString("\n" + styleMuted.Render("[⏎] import   [tab] one file / a folder   [esc] back"))

	case importCaps:
		b.WriteString(styleMuted.Render(
			"A recursive import is capped so that pointing it at a home directory\n"+
				"fails at once instead of copying for ten minutes. Raise the caps for\n"+
				"THIS import — the refusal above states the tree's real size.") + "\n\n")
		b.WriteString(capField("max files", v.maxFiles, v.capField == 0) + "\n")
		b.WriteString(capField("max size ", v.maxBytes, v.capField == 1) + "\n")
		b.WriteString("\n" + styleMuted.Render("blank = the daemon's default (2000 files / 512 MiB)") + "\n")
		if v.capParseErr != "" {
			b.WriteString("\n" + styleErrored.Render(v.capParseErr) + "\n")
		}
		b.WriteString("\n" + styleMuted.Render("[⏎] import   [tab] switch field   [esc] back"))

	case importRunning:
		b.WriteString(styleAccent.Render("⏳ importing — each file is ledgered before it crosses…"))

	case importDone:
		if v.err != "" {
			b.WriteString(styleErrored.Render(v.err) + "\n")
			b.WriteString("\n" + styleMuted.Render("[c] raise the caps   [r] try again   [esc] close"))
			return b.String()
		}
		res := v.result
		if !res.Recursive {
			b.WriteString(fmt.Sprintf("imported 1 file (%s)\n  → %s\n", humanBytes(uint64(res.Bytes)), res.Dest))
		} else {
			b.WriteString(fmt.Sprintf("%s crossed (%s)\n  → %s\n",
				countNoun(len(res.Files), "file"), humanBytes(uint64(res.Bytes)), res.Dest))
			b.WriteString(styleMuted.Render("  ledger batch "+res.BatchID) + "\n")
		}
		for _, s := range res.Skipped {
			b.WriteString(styleMuted.Render(fmt.Sprintf("  skipped %s — %s", s.Rel, s.Reason)) + "\n")
		}
		// Failures are rendered LAST and in the alarm style, and the "nothing was
		// rolled back" line is not optional: a partial import leaves real files in
		// place, and a reader who assumes it was undone will import again and
		// wonder why the counts differ.
		if len(res.Failed) > 0 {
			b.WriteString("\n" + styleErrored.Render(fmt.Sprintf("%s did NOT cross:", countNoun(len(res.Failed), "file"))) + "\n")
			for _, f := range res.Failed {
				b.WriteString(styleErrored.Render(fmt.Sprintf("  %s — %s", f.Rel, f.Error)) + "\n")
			}
			b.WriteString(styleMuted.Render("what crossed is still there — nothing was rolled back") + "\n")
		}
		b.WriteString("\n" + styleMuted.Render("[r] import more   [esc] close"))
	}
	return b.String()
}

// capOrDefault renders a cap field for display, saying "default" rather than
// showing an empty string — a blank next to the word "caps" reads as "no cap".
func capOrDefault(v string) string {
	if strings.TrimSpace(v) == "" {
		return "default"
	}
	return strings.TrimSpace(v)
}

// capField renders one editable cap field with the cursor on the active one.
func capField(label, val string, active bool) string {
	cur := ""
	if active {
		cur = "_"
	}
	line := "  " + label + "  " + val + cur
	if active {
		return styleAccent.Render(line)
	}
	return styleMuted.Render(line)
}
