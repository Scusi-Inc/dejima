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
// Adding happens inline, in two masked-friendly steps (name, then value). It
// stays in the dashboard rather than shelling out to a window: the value field
// is masked here just as a hidden-input prompt would be, and keeping it in-pane
// means one flow instead of a second process the operator has to find.
type secretsView struct {
	island  string
	loading bool
	secrets []secrets.Meta
	cursor  int // highlighted row, for remove
	err     string
	notice  string

	// Inline add/rotate. addPhase: 0 = typing the name, 1 = typing the value.
	adding    bool
	addPhase  int
	nameInput string
	valInput  string

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
	// added names a secret just set, so the pane can show the restart notice
	// and a confirmation only on a real change (not on plain reloads).
	added string
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

// putSecretCmd sets or rotates a secret, then reloads. The value is dropped the
// moment the request is built — it lives no longer in the client than it must.
func (m tuiModel) putSecretCmd(island, name, value string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := c.PutSecret(ctx, island, name, value); err != nil {
			return islandSecretsMsg{island: island, err: err}
		}
		list, lerr := c.ListSecrets(ctx, island)
		return islandSecretsMsg{island: island, secrets: list, err: lerr, added: name}
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

// secretsKey drives the pane: navigate, add/rotate inline, remove, reload.
func (m tuiModel) secretsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.secretsPane

	// The inline add form owns keys while active.
	if v.adding {
		return m.secretsAddKey(msg)
	}

	switch msg.String() {
	case "esc", "q":
		m.secretsPane = nil
		return m, nil
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
		}
		return m, nil
	case "down", "j":
		if v.cursor < len(v.secrets)-1 {
			v.cursor++
		}
		return m, nil
	case "r":
		v.loading, v.notice, v.err = true, "", ""
		return m, m.loadSecretsCmd(v.island)
	case "a":
		v.adding, v.addPhase = true, 0
		v.nameInput, v.valInput, v.err, v.notice = "", "", "", ""
		return m, nil
	case "x":
		if len(v.secrets) == 0 {
			return m, nil
		}
		name := v.secrets[v.cursor].Name
		m.confirm = &confirmPrompt{verb: "remove-secret", island: v.island, agent: name, answer: ""}
		return m, nil
	}
	return m, nil
}

// secretsAddKey drives the two-step inline add: type a NAME, Enter, type a
// VALUE (masked), Enter to store.
func (m tuiModel) secretsAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.secretsPane
	switch msg.String() {
	case "esc":
		v.adding = false
		v.nameInput, v.valInput = "", ""
		return m, nil
	case "enter":
		if v.addPhase == 0 {
			name := strings.TrimSpace(v.nameInput)
			if name == "" {
				return m, nil
			}
			// Validate here so the operator gets a reserved-name error before
			// typing the value, not after. The daemon enforces it too.
			if err := secrets.ValidateName(name); err != nil {
				v.err = err.Error()
				return m, nil
			}
			v.nameInput, v.err = name, ""
			v.addPhase = 1
			return m, nil
		}
		// Phase 1: submit. Blank value is a no-op rather than an error — Enter on
		// an empty field reads as "never mind".
		if v.valInput == "" {
			v.adding = false
			return m, nil
		}
		name, value := v.nameInput, v.valInput
		v.adding, v.nameInput, v.valInput = false, "", ""
		v.loading = true
		return m, m.putSecretCmd(v.island, name, value)
	case "backspace":
		if v.addPhase == 0 && v.nameInput != "" {
			v.nameInput = v.nameInput[:len(v.nameInput)-1]
		} else if v.addPhase == 1 && v.valInput != "" {
			v.valInput = v.valInput[:len(v.valInput)-1]
		}
		return m, nil
	default:
		if s := msg.String(); len(s) == 1 {
			if v.addPhase == 0 {
				v.nameInput += s
			} else {
				v.valInput += s
			}
		}
		return m, nil
	}
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

	// Inline add form takes over the body while active.
	if v.adding {
		b.WriteString(styleMuted.Render("Add or rotate a secret for " + v.island + ".\n\n"))
		if v.addPhase == 0 {
			b.WriteString("name:  " + styleAccent.Render(v.nameInput+"▏"))
			b.WriteString("\n\n" + styleMuted.Render("An environment-variable name, e.g. EXPO_TOKEN. [⏎] next   [esc] cancel"))
		} else {
			b.WriteString(styleMuted.Render("name:  ") + v.nameInput)
			b.WriteString("\n")
			// Mask the value: length only, never the characters. The pane can be
			// attached from more than one device, so the value is treated like a
			// password field.
			b.WriteString("value: " + styleAccent.Render(strings.Repeat("•", len(v.valInput))+"▏"))
			b.WriteString("\n\n" + styleMuted.Render("Hidden as you type. [⏎] store   [esc] cancel"))
		}
		return b.String()
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
		for i, s := range v.secrets {
			rotated := styleMuted.Render("—")
			if s.UpdatedAt.After(s.CreatedAt) {
				rotated = s.UpdatedAt.Local().Format("2006-01-02")
			}
			marker := "  "
			name := s.Name
			if i == v.cursor {
				marker = styleAccent.Render("▶ ")
				name = styleAccent.Render(s.Name)
			}
			row := fmt.Sprintf("%-24s %-12s %-12s %s",
				name, styleMuted.Render(s.Fingerprint),
				s.CreatedAt.Local().Format("2006-01-02"), rotated)
			b.WriteString(marker + row + "\n")
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
	b.WriteString(styleMuted.Render("[↑/↓] select   [a] add/rotate   [x] remove   [r] reload   [esc] back"))
	return b.String()
}
