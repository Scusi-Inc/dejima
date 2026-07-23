package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/secrets"
)

// secretsView is the per-island Secrets pane: which secrets exist, when they
// were added and last rotated, and their fingerprints. Values are never shown —
// there is no key that reveals one, because the API never returns one.
//
// Adding a value happens in a separate window via the tested `dejima secret set`
// CLI, for the same reason GitHub sign-in does: a hidden-input prompt belongs in
// a terminal, not in a dashboard that redraws.
type secretsView struct {
	island  string
	loading bool
	secrets []secrets.Meta
	err     string
	notice  string

	// restartPending is set after a change lands. A running process cannot have
	// its environment altered, so a rotation reaches new shells only — without
	// saying so, an operator watches their agent keep failing with the old value
	// and concludes the feature is broken.
	restartPending bool
}

type islandSecretsMsg struct {
	island  string
	secrets []secrets.Meta
	err     error
}

func (m tuiModel) openSecretsView(island string) (tea.Model, tea.Cmd) {
	if island == "" {
		m.lastNotice = "select an island first — secrets are per-island"
		return m, nil
	}
	m.secretsPane = &secretsView{island: island, loading: true}
	return m, m.loadSecretsCmd(island)
}

func (m tuiModel) loadSecretsCmd(island string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list, err := c.ListSecrets(ctx, island)
		return islandSecretsMsg{island: island, secrets: list, err: err}
	}
}

// removeSecretCmd deletes a secret and reloads the pane.
func (m tuiModel) removeSecretCmd(island, key string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := c.DeleteSecret(ctx, island, key); err != nil {
			return islandSecretsMsg{island: island, err: err}
		}
		list, err := c.ListSecrets(ctx, island)
		return islandSecretsMsg{island: island, secrets: list, err: err}
	}
}

// secretsKey drives the pane. [a] add (new window), [x] remove, [r] reload.
func (m tuiModel) secretsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.secretsPane
	switch msg.String() {
	case "esc", "q":
		m.secretsPane = nil
		return m, nil
	case "r":
		v.loading, v.notice, v.err = true, "", ""
		return m, m.loadSecretsCmd(v.island)
	case "a":
		if !canOpenNewWindow() {
			v.notice = "run `dejima secret set " + v.island + " <NAME>` in a terminal"
			return m, nil
		}
		if err := m.openSecretSetWindow(v.island); err != nil {
			v.notice = "run `dejima secret set " + v.island + " <NAME>` in a terminal"
		} else {
			v.notice = "adding a secret in a new window — press [r] to reload when done"
		}
		return m, nil
	case "x":
		// Removing needs a name; with none listed there's nothing to do.
		if len(v.secrets) == 0 {
			return m, nil
		}
		m.confirm = &confirmPrompt{
			verb: "remove-secret", island: v.island, agent: v.secrets[0].Name, answer: "",
		}
		return m, nil
	}
	return m, nil
}

func (v *secretsView) view(width int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Secrets — " + v.island))
	b.WriteString("\n\n")

	if v.loading {
		b.WriteString(styleAccent.Render("⏳ loading…"))
		return b.String()
	}
	if v.err != "" {
		b.WriteString(styleErrored.Render("✗ " + v.err))
		b.WriteString("\n\n")
	}

	if len(v.secrets) == 0 {
		b.WriteString(styleMuted.Render("No secrets on this island yet."))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("Secrets are the tokens your tools read from the environment —\nEXPO_TOKEN, NPM_TOKEN, API keys."))
		b.WriteString("\n\n")
	} else {
		b.WriteString(fmt.Sprintf("  %-24s %-12s %-12s %s\n",
			styleMuted.Render("NAME"), styleMuted.Render("FINGERPRINT"),
			styleMuted.Render("ADDED"), styleMuted.Render("ROTATED")))
		for _, s := range v.secrets {
			rotated := styleMuted.Render("—")
			if s.UpdatedAt.After(s.CreatedAt) {
				rotated = s.UpdatedAt.Local().Format("2006-01-02")
			}
			b.WriteString(fmt.Sprintf("  %-24s %-12s %-12s %s\n",
				s.Name, styleMuted.Render(s.Fingerprint),
				s.CreatedAt.Local().Format("2006-01-02"), rotated))
		}
		b.WriteString("\n")
	}

	if v.restartPending {
		// Loud on purpose: this is the single thing most likely to make the
		// feature look broken when it is working correctly.
		b.WriteString(styleWaiting.Render("⚠  RESTART TERMINALS TO APPLY"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("   Live in NEW shells. Anything already running still has the old\n   environment — restart the agent to pick it up."))
		b.WriteString("\n\n")
	}

	// The honest caveat, in the UI rather than buried in docs. An operator who
	// believes these are hidden from agents will store things that don't belong
	// in an agent's container — which is worse than having no feature at all.
	b.WriteString(styleMuted.Render("Values are never shown. Fingerprint is sha256(value)[:8] — hash your copy to compare."))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("Agents in this island CAN read these from their environment; this keeps them out\nof your repo and gives you one place to rotate and revoke. Prefer scoped tokens."))
	b.WriteString("\n\n")

	if v.notice != "" {
		b.WriteString(styleRunning.Render("✓ " + v.notice))
		b.WriteString("\n\n")
	}
	b.WriteString(styleMuted.Render("[a] add/rotate   [x] remove   [r] reload   [esc] back"))
	return b.String()
}
