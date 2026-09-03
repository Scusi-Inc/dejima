package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

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

	field   int // 0 provider · 1 model · 2 key · 3 save
	provSel int
	model   string // editable

	keySet   bool   // whether a key exists for the selected provider
	keyInput string // typed key, masked on render; never echoed
	busy     bool

	loadedProvider   string // provider/model at load, to detect a change on save
	loadedModel      string
	applyCfgAfterKey bool // a save that stored a key, then applies provider/model
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
	// Baseline for change detection on save.
	ed.loadedProvider = msg.curProvider
	ed.loadedModel = ed.model
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

// fieldSave is the index of the Save action row (below provider/model/key).
const fieldSave = 3

func (m tuiModel) modelEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := m.modelEditor
	key := msg.String()
	if key == "esc" || key == "ctrl+c" {
		m.modelEditor = nil
		return m, nil
	}
	if ed.loading || ed.busy {
		return m, nil
	}
	// Navigation and commit are the same on every field: arrows move between
	// fields (never j/k — those are valid key/model characters), Enter saves. So
	// the key/model fields accept typing/paste immediately, with no "press Enter
	// to start editing" step.
	switch key {
	case "up":
		if ed.field > 0 {
			ed.field--
		}
		return m, nil
	case "down":
		if ed.field < fieldSave {
			ed.field++
		}
		return m, nil
	case "enter":
		return m.saveModelEditor()
	}
	// Field-specific input.
	switch ed.field {
	case 0: // provider cycler
		switch key {
		case "left", "h":
			if len(ed.providers) > 0 {
				ed.provSel = (ed.provSel - 1 + len(ed.providers)) % len(ed.providers)
				ed.refreshKeySet()
			}
		case "right", "l":
			if len(ed.providers) > 0 {
				ed.provSel = (ed.provSel + 1) % len(ed.providers)
				ed.refreshKeySet()
			}
		}
	case 1: // model: free text (type or paste)
		if key == "backspace" {
			if len(ed.model) > 0 {
				ed.model = ed.model[:len(ed.model)-1]
			}
		} else {
			ed.model += pastableInput(msg)
		}
	case 2: // API key: type or paste directly, no enter-to-edit
		if key == "backspace" {
			if len(ed.keyInput) > 0 {
				ed.keyInput = ed.keyInput[:len(ed.keyInput)-1]
			}
		} else {
			ed.keyInput += pastableInput(msg)
		}
	}
	return m, nil
}

// saveModelEditor commits the editor: stores a typed key (account-wide) and/or
// applies a changed provider/model, then closes. Enter triggers it from any
// field, and it's the highlighted Save action at the bottom.
func (m tuiModel) saveModelEditor() (tea.Model, tea.Cmd) {
	ed := m.modelEditor
	if ed.currentProvider() == "" {
		m.lastError = "pick a provider first"
		return m, nil
	}
	hasKey := strings.TrimSpace(ed.keyInput) != ""
	cfgChanged := ed.currentProvider() != ed.loadedProvider ||
		strings.TrimSpace(ed.model) != strings.TrimSpace(ed.loadedModel)
	if cfgChanged && strings.TrimSpace(ed.model) == "" {
		m.lastError = "enter a model (e.g. " + firstOr(ed.suggested, "anthropic/claude-sonnet-4-6") + ")"
		return m, nil
	}
	switch {
	case hasKey:
		// Store the key first; the providerKeySetMsg handler applies the config
		// after (if it changed) or closes.
		ed.applyCfgAfterKey = cfgChanged
		ed.busy = true
		return m, m.setProviderKeyCmd(ed.currentProvider(), ed.keyInput)
	case cfgChanged:
		ed.busy = true
		return m, m.applyAgentConfigCmd(ed.island, ed.agentID, ed.currentProvider(), ed.model)
	default:
		m.modelEditor = nil // nothing to change
		return m, nil
	}
}

// pastableInput returns the printable text a key event contributes to a text
// field — a single typed rune OR a whole bracketed paste (KeyRunes carries the
// full pasted string), control characters filtered out. Editing/navigation keys
// contribute nothing.
func pastableInput(msg tea.KeyMsg) string {
	var b strings.Builder
	for _, r := range msg.Runes {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
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
	// API key: masked. When the field is focused you type/paste straight in — no
	// "press Enter to edit" step; the cursor shows it's live.
	keyVal := styleErrored.Render("not set")
	if ed.keySet {
		keyVal = "set"
	}
	switch {
	case len(ed.keyInput) > 0:
		keyVal = strings.Repeat("•", len(ed.keyInput))
		if ed.field == 2 {
			keyVal += "▌"
		}
	case ed.field == 2:
		keyVal = styleMuted.Render("type or paste the key ") + "▌"
	}
	modelVal := ed.model
	if ed.field == 1 {
		modelVal += "▌"
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

	if ed.busy {
		b.WriteString("\n" + styleAccent.Render("saving…"))
		return b.String()
	}

	// Save action row — navigate to it and ⏎, or ⏎ from any field. Highlighted
	// when selected so "scroll down and hit Enter to save" is obvious.
	if ed.field == fieldSave {
		b.WriteString(styleAccent.Render(" ▸ ") + styleSelected.Render(" Save (⏎) ") + "    " + styleMuted.Render("esc cancel"))
	} else {
		b.WriteString("   " + styleMuted.Render("[Save (⏎)]") + "    " + styleMuted.Render("esc cancel"))
	}
	b.WriteString("\n\n")

	if len(ed.suggested) > 0 {
		b.WriteString(styleMuted.Render("suggested models: " + strings.Join(ed.suggested, ", ")))
		b.WriteString("\n")
	}
	if ed.currentProvider() != "" && !ed.keySet && len(ed.keyInput) == 0 {
		b.WriteString(styleWaiting.Render("⚠ no key for " + ed.currentProvider() + " — select the API key field and type or paste one"))
		b.WriteString("\n")
	}
	b.WriteString(styleMuted.Render("↑/↓ field · ←/→ provider · type/paste the model & key · ⏎ save · esc cancel"))
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
