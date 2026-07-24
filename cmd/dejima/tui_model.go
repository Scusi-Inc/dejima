package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/api"
	tea "github.com/charmbracelet/bubbletea"
)

// modelEditor is the per-agent LLM provider/model/key overlay (opened with `v`
// on an island or agent row, for key-requiring agent types like openclaw). It
// mirrors resourceEditor's shape: a small field list applied async, reusing the
// recreate-to-apply confirm. Three fields: provider (cycler), model (free text
// with suggestions), and the API key (set/not-set, with masked inline entry).
type modelEditor struct {
	island    string
	agentID   string
	agentType string

	loading   bool
	providers []string // from the handler's SupportedProviders (fallback: configured providers)
	suggested []string // SuggestedModels (hints only)
	keyStatus map[string]bool

	field   int // 0 provider · 1 model · 2 key
	provSel int
	model   string // editable

	keySet bool // whether a key exists for the selected provider

	enteringKey bool
	keyInput    string // masked on render; never echoed
	busy        bool
}

func (m tuiModel) openModelEditor(island, agentID string) (tea.Model, tea.Cmd) {
	isl, ok := m.islandByName(island)
	if !ok || len(isl.Agents) == 0 {
		m.lastError = "no agent to configure here"
		return m, nil
	}
	if agentID == "" {
		agentID = isl.Agents[0].ID // island row → its primary agent
	}
	a := agentByID(isl, agentID)
	ed := &modelEditor{island: island, agentID: agentID, agentType: a.Type, loading: true, model: a.Model}
	m.modelEditor = ed
	return m, m.loadModelEditorCmd(island, agentID, a.Type, a.Provider)
}

type modelEditorLoadedMsg struct {
	agentID     string
	err         error
	cap         api.AgentTypeCapability
	keyStatus   map[string]bool
	curProvider string
}

// loadModelEditorCmd fetches the agent type's capabilities + which providers
// have a stored key, so the overlay can populate its picker. It rejects (via
// err) agent types that don't use a provider key.
func (m tuiModel) loadModelEditorCmd(island, agentID, agentType, curProvider string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		types, err := c.ListAgentTypes(ctx)
		if err != nil {
			return modelEditorLoadedMsg{agentID: agentID, err: err}
		}
		var capab api.AgentTypeCapability
		found := false
		for _, t := range types {
			if t.Type == agentType {
				capab, found = t, true
				break
			}
		}
		if !found || !capab.RequiresProviderKey {
			return modelEditorLoadedMsg{agentID: agentID,
				err: fmt.Errorf("agent type %q doesn't use a model key", agentType)}
		}
		keyStatus := map[string]bool{}
		if creds, err := c.ListProviderCredentials(ctx); err == nil {
			for _, p := range creds {
				keyStatus[p.Name] = p.KeySet
			}
		}
		return modelEditorLoadedMsg{agentID: agentID, cap: capab, keyStatus: keyStatus, curProvider: curProvider}
	}
}

// applyLoaded fills the editor from the capability + credential snapshot.
func (ed *modelEditor) applyLoaded(msg modelEditorLoadedMsg) {
	ed.loading = false
	ed.suggested = msg.cap.SuggestedModels
	ed.keyStatus = msg.keyStatus
	ed.providers = msg.cap.SupportedProviders
	if len(ed.providers) == 0 { // no advisory list → offer the configured providers
		for name := range msg.keyStatus {
			ed.providers = append(ed.providers, name)
		}
		sort.Strings(ed.providers)
	}
	// Preselect the provider: the agent's configured provider, else the one
	// encoded in its model string ("openai/gpt-5.5" → openai), so the picker opens
	// on the right provider instead of always the first.
	want := msg.curProvider
	if want == "" {
		if i := strings.IndexByte(ed.model, '/'); i > 0 {
			want = ed.model[:i]
		}
	}
	for i, p := range ed.providers {
		if p == want {
			ed.provSel = i
			break
		}
	}
	ed.refreshKeySet()
}

func (ed *modelEditor) refreshKeySet() {
	if p := ed.currentProvider(); p != "" {
		ed.keySet = ed.keyStatus[p]
	} else {
		ed.keySet = false
	}
}

func (ed *modelEditor) currentProvider() string {
	if ed.provSel >= 0 && ed.provSel < len(ed.providers) {
		return ed.providers[ed.provSel]
	}
	return ""
}

func (m tuiModel) modelEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := m.modelEditor
	key := msg.String()
	if key == "esc" || key == "ctrl+c" {
		if ed.enteringKey { // cancel key entry, not the whole overlay
			ed.enteringKey, ed.keyInput = false, ""
			return m, nil
		}
		m.modelEditor = nil
		return m, nil
	}
	if ed.loading || ed.busy {
		return m, nil
	}

	// Masked key entry sub-mode owns input.
	if ed.enteringKey {
		switch key {
		case "enter":
			if strings.TrimSpace(ed.keyInput) == "" {
				ed.enteringKey = false
				return m, nil
			}
			ed.busy = true
			return m, m.setProviderKeyCmd(ed.currentProvider(), ed.keyInput)
		case "backspace":
			if len(ed.keyInput) > 0 {
				ed.keyInput = ed.keyInput[:len(ed.keyInput)-1]
			}
		default:
			if len(key) == 1 {
				ed.keyInput += key
			}
		}
		return m, nil
	}

	switch ed.field {
	case 1: // model: free text
		switch key {
		case "up", "k":
			ed.field = 0
		case "down", "j":
			ed.field = 2
		case "enter":
			return m.applyModelEditor()
		case "backspace":
			if len(ed.model) > 0 {
				ed.model = ed.model[:len(ed.model)-1]
			}
		default:
			if len(key) == 1 {
				ed.model += key
			}
		}
		return m, nil
	default: // provider (0) or key (2)
		switch key {
		case "up", "k":
			if ed.field > 0 {
				ed.field--
			}
		case "down", "j":
			if ed.field < 2 {
				ed.field++
			}
		case "left", "h":
			if ed.field == 0 && len(ed.providers) > 0 {
				ed.provSel = (ed.provSel - 1 + len(ed.providers)) % len(ed.providers)
				ed.refreshKeySet()
			}
		case "right", "l", " ":
			if ed.field == 0 && len(ed.providers) > 0 {
				ed.provSel = (ed.provSel + 1) % len(ed.providers)
				ed.refreshKeySet()
			}
		case "enter":
			if ed.field == 2 { // begin masked key entry for the selected provider
				if ed.currentProvider() == "" {
					m.lastError = "pick a provider first"
					return m, nil
				}
				ed.enteringKey, ed.keyInput = true, ""
				return m, nil
			}
			return m.applyModelEditor()
		}
		return m, nil
	}
}

// applyModelEditor PATCHes the agent's provider/model.
func (m tuiModel) applyModelEditor() (tea.Model, tea.Cmd) {
	ed := m.modelEditor
	if ed.currentProvider() == "" {
		m.lastError = "pick a provider"
		return m, nil
	}
	if strings.TrimSpace(ed.model) == "" {
		m.lastError = "enter a model (e.g. " + firstOr(ed.suggested, "anthropic/claude-sonnet-4-6") + ")"
		return m, nil
	}
	ed.busy = true
	return m, m.applyAgentConfigCmd(ed.island, ed.agentID, ed.currentProvider(), ed.model)
}

type agentConfiguredMsg struct {
	err             error
	restartRequired bool
}

func (m tuiModel) applyAgentConfigCmd(island, agentID, provider, model string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := c.ConfigureAgent(ctx, island, agentID, api.AgentConfigRequest{Provider: &provider, Model: &model})
		return agentConfiguredMsg{err: err, restartRequired: resp != nil && resp.RestartRequired}
	}
}

type providerKeySetMsg struct {
	provider string
	err      error
}

func (m tuiModel) setProviderKeyCmd(provider, key string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := c.PutProviderCredential(ctx, provider, api.PutProviderCredentialRequest{APIKey: key})
		return providerKeySetMsg{provider: provider, err: err}
	}
}

func (m tuiModel) renderModelEditor() string {
	ed := m.modelEditor
	var b strings.Builder
	b.WriteString(styleHeader.Render("Model / provider / key — " + ed.island + "/" + ed.agentID))
	b.WriteString("\n")
	if ed.loading {
		b.WriteString("\n" + styleAccent.Render("loading…"))
		return b.String()
	}
	// Head off the common confusion ("where's the OpenClaw provider?"): the
	// provider is the LLM backend the agent talks to, not the agent framework.
	who := ed.agentType
	if who == "" {
		who = "this agent"
	}
	b.WriteString(styleMuted.Render("Provider = the LLM " + who + " talks to (openai · anthropic · google) — not " + who + " itself."))
	b.WriteString("\n\n")

	prov := ed.currentProvider()
	if prov == "" {
		prov = styleMuted.Render("(no providers)")
	}
	keyVal := styleErrored.Render("not set")
	if ed.keySet {
		keyVal = "set"
	}
	if ed.enteringKey {
		keyVal = strings.Repeat("•", len(ed.keyInput)) + styleMuted.Render(" ⏎ save · esc cancel")
	}
	modelVal := ed.model
	if ed.field == 1 {
		modelVal += "_"
	}
	if modelVal == "" {
		modelVal = styleMuted.Render("(required)")
	}

	rows := [3]struct{ label, val string }{
		{"LLM provider", prov},
		{"Model", modelVal},
		{"API key", keyVal},
	}
	for i, r := range rows {
		lead := "   "
		val := r.val
		if i == 0 {
			val = styleMuted.Render("‹ ") + r.val + styleMuted.Render(" ›")
		}
		if i == ed.field {
			lead = styleAccent.Render(" ▸ ")
			if i == 0 {
				val = styleSelected.Render(" ‹ " + r.val + " › ")
			}
		}
		b.WriteString(fmt.Sprintf("%s%-13s %s\n", lead, r.label, val))
	}
	b.WriteString("\n")
	if ed.busy {
		b.WriteString(styleAccent.Render("applying…"))
		return b.String()
	}
	if len(ed.suggested) > 0 {
		b.WriteString(styleMuted.Render("suggested models: " + strings.Join(ed.suggested, ", ")))
		b.WriteString("\n")
	}
	if prov != "" && !ed.keySet && !ed.enteringKey {
		b.WriteString(styleWaiting.Render("⚠ no key for " + ed.currentProvider() + " — select the API key field and press ⏎ to set one"))
		b.WriteString("\n")
	}
	b.WriteString(styleMuted.Render("↑/↓ field · ←/→ provider · type the model · ⏎ apply · esc cancel"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("the key is account-wide — it applies to every agent on this provider"))
	return b.String()
}

func firstOr(s []string, fallback string) string {
	if len(s) > 0 {
		return s[0]
	}
	return fallback
}
