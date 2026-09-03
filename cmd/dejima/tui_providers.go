package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/providercreds"
)

// The provider-keys settings page: see which LLM providers the daemon holds a
// key for, and set or rotate one.
//
// Until now the TUI could collect a provider key in exactly one place — the
// add-an-agent flow, and only when the agent being added had no key. After that
// there was no way to add or rotate one at all, so an operator whose Anthropic
// key expired watched OpenClaw fail with HTTP 401 and had to drop to the CLI to
// fix it. The daemon has had the endpoints the whole time.

type providerCredsMsg struct {
	creds []providercreds.Meta
	cands []string
	err   error
}

type providerKeySavedMsg struct {
	provider string
	creds    []providercreds.Meta
	err      error
}

// fetchProviderCredsCmd loads the stored credentials AND the set of providers an
// agent could use.
//
// Both, because they answer different questions and the page needs each. The
// stored list is empty on a fresh daemon, so a page built from it alone can
// never offer the first key — the exact dead end this page exists to remove.
func (m tuiModel) fetchProviderCredsCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		creds, err := c.ListProviderCredentials(ctx)
		if err != nil {
			return providerCredsMsg{err: err}
		}
		return providerCredsMsg{creds: creds, cands: providerCandidates(ctx, c, creds)}
	}
}

// providerCandidates is every provider worth offering: the ones a key-requiring
// agent type declares support for, unioned with whatever is already stored (so a
// provider configured by hand never vanishes from the list just because no agent
// type names it).
func providerCandidates(ctx context.Context, c *api.Client, creds []providercreds.Meta) []string {
	types, err := c.ListAgentTypes(ctx)
	if err != nil {
		types = nil // still offer whatever is stored; never fail the whole page
	}
	return unionProviders(creds, types)
}

// unionProviders merges the providers that HAVE a key with the providers an
// agent COULD use. Pure, so the union is testable without a daemon — the fetch
// above is the part that cannot be.
//
// Both halves are load-bearing. Without the agent types, a fresh daemon offers
// nothing and the first key can never be added. Without the stored names, a
// provider configured by hand vanishes from the list the moment no agent type
// happens to declare it.
func unionProviders(creds []providercreds.Meta, types []api.AgentTypeCapability) []string {
	seen := map[string]bool{}
	for _, m := range creds {
		seen[m.Name] = true
	}
	for _, t := range types {
		for _, p := range t.SupportedProviders {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// saveProviderKeyCmd stores a key and returns the refreshed list, so the page
// repaints from the daemon's answer rather than from what we hoped it did.
func (m tuiModel) saveProviderKeyCmd(provider, key string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		creds, err := c.PutProviderCredential(ctx, provider, api.PutProviderCredentialRequest{APIKey: key})
		return providerKeySavedMsg{provider: provider, creds: creds, err: err}
	}
}

// settingsProvidersKey owns every key on this page.
//
// It has to: j, k and q are ordinary characters in an API key, and the shared
// settings handler binds them to move-down, move-up and close. Providers are
// selected with the ARROW keys only, exactly as agentAdderKeyStep does for the
// same reason.
func (m tuiModel) settingsProvidersKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	if s.provBusy {
		return m, nil // a save is in flight; swallow input rather than queue it
	}
	switch msg.String() {
	case "esc", "ctrl+[", "ctrl+c", "left":
		if s.provInput != "" {
			s.provInput, s.provNotice = "", "" // first esc clears a half-typed key
			return m, nil
		}
		s.page, s.sel = settingsTop, 0
		return m, nil
	case "up":
		if s.provSel > 0 {
			s.provSel--
		}
		return m, nil
	case "down":
		if s.provSel < len(s.provCands)-1 {
			s.provSel++
		}
		return m, nil
	case "backspace":
		if s.provInput != "" {
			s.provInput = s.provInput[:len(s.provInput)-1]
		}
		return m, nil
	case "enter":
		if s.provSel >= len(s.provCands) {
			return m, nil
		}
		key := strings.TrimSpace(s.provInput)
		if key == "" {
			s.provErr = "type or paste a key first — ↑/↓ picks the provider"
			return m, nil
		}
		provider := s.provCands[s.provSel]
		s.provBusy, s.provErr, s.provNotice = true, "", "saving…"
		s.provInput = ""
		return m, m.saveProviderKeyCmd(provider, key)
	default:
		// pastableInput, never a byte-length test: that accepts NUL (a Windows
		// paste arrives character-by-character, leading NUL included) and silently
		// drops every non-ASCII rune. This is an API-key field, where a dropped
		// character stores a credential that is wrong and looks stored.
		if in := pastableInput(msg); in != "" {
			s.provInput += in
			s.provErr = ""
		}
		return m, nil
	}
}

// renderProviders draws the page. The key is NEVER echoed — only its length.
func (m tuiModel) renderProviders() string {
	s := m.settings
	var b strings.Builder
	b.WriteString(styleHeader.Render("Provider keys"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("LLM API keys the daemon holds. Islands get the key for the provider their"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("agent uses; an already-running agent needs a restart to pick up a change."))
	b.WriteString("\n\n")

	switch {
	case s.provErr != "":
		b.WriteString(styleErrored.Render("  ⚠ " + truncate(s.provErr, 72)))
		b.WriteString("\n\n")
	case s.provNotice != "":
		b.WriteString(styleRunning.Render("  " + truncate(s.provNotice, 72)))
		b.WriteString("\n\n")
	}

	if s.provCands == nil && s.provErr == "" {
		b.WriteString(styleMuted.Render("  loading…"))
		return b.String()
	}

	byName := map[string]providercreds.Meta{}
	for _, c := range s.provCreds {
		byName[c.Name] = c
	}
	for i, name := range s.provCands {
		// A provider with no key must SAY so. Rendering it blank reads as missing
		// data rather than as the finding, and "no key" is the whole reason
		// someone opened this page.
		state := styleErrored.Render("not set")
		if c, ok := byName[name]; ok && c.KeySet {
			state = "set"
			if c.Hint != "" {
				state += styleMuted.Render("  " + c.Hint)
			}
			if c.Default {
				state += styleMuted.Render("  · default")
			}
			if !c.UpdatedAt.IsZero() {
				state += styleMuted.Render("  · " + timeAgo(c.UpdatedAt) + " ago")
			}
		}
		line := fmt.Sprintf("%-12s %s", name, state)
		if i == s.provSel {
			b.WriteString(styleSelected.Render("▶ ") + line)
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	field := styleMuted.Render("type or paste a key ") + "▌"
	if len(s.provInput) > 0 {
		field = styleAccent.Render(strings.Repeat("•", len(s.provInput)) + "▌")
	}
	b.WriteString("  key: " + field + "\n\n")
	b.WriteString(styleMuted.Render("[↑/↓] provider · type the key (hidden) · [⏎] save · [esc] back"))
	return b.String()
}
