package main

import (
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/providercreds"
)

func provModel(creds []providercreds.Meta, cands ...string) tuiModel {
	return tuiModel{settings: &settingsModel{
		page: settingsProviders, provCreds: creds, provCands: cands,
	}}
}

// The page must offer a provider that has NO key yet.
//
// ListProviderCredentials returns nothing on a fresh daemon, so a page built
// from the stored list alone could never add the first key — which is the dead
// end this page exists to remove.
func TestProvidersPageOffersUnconfiguredProviders(t *testing.T) {
	m := provModel(nil, "anthropic", "google", "openai")
	view := m.renderProviders()
	for _, p := range []string{"anthropic", "google", "openai"} {
		if !strings.Contains(view, p) {
			t.Errorf("provider %q is not offered; with no stored keys the page would be empty "+
				"and there would be no way to add the first one", p)
		}
	}
	if !strings.Contains(view, "not set") {
		t.Errorf("a provider with no key does not say so — a blank cell reads as missing "+
			"data, and 'no key' is the finding:\n%s", view)
	}
}

// A stored key is shown by its masked hint and never by its value.
func TestProvidersPageMasksAndNeverEchoesTheKey(t *testing.T) {
	m := provModel([]providercreds.Meta{{
		Name: "anthropic", KeySet: true, Hint: "…a1b2", Default: true,
		UpdatedAt: time.Now().Add(-3 * time.Hour),
	}}, "anthropic", "openai")
	view := m.renderProviders()
	for _, want := range []string{"set", "…a1b2", "default", "3h ago"} {
		if !strings.Contains(view, want) {
			t.Errorf("the row omits %q — these are what tell two keys apart:\n%s", want, view)
		}
	}

	// And a key being TYPED is bullets, never characters.
	secret := "sk-ant-supersecret"
	for _, r := range secret {
		out, _ := m.settingsProvidersKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(tuiModel)
	}
	if got := m.settings.provInput; got != secret {
		t.Fatalf("the field did not capture the key: %q", got)
	}
	view = m.renderProviders()
	if strings.Contains(view, secret) || strings.Contains(view, "supersecret") {
		t.Errorf("THE KEY IS RENDERED IN THE CLEAR:\n%s", view)
	}
	if !strings.Contains(view, strings.Repeat("•", len(secret))) {
		t.Errorf("the key is not masked to its length:\n%s", view)
	}
}

// j, k and q are ordinary API-key characters. The shared settings handler binds
// them to move-down, move-up and close, so this page must own its keys — the
// same reason agentAdderKeyStep navigates with arrows only.
func TestProvidersPageTypesJKQInsteadOfNavigating(t *testing.T) {
	m := provModel(nil, "anthropic", "openai", "google")
	for _, k := range []string{"j", "k", "q"} {
		out, _ := m.settingsKey(key(k))
		m = out.(tuiModel)
	}
	if m.settings == nil {
		t.Fatal("typing `q` into the key field CLOSED the settings overlay")
	}
	if m.settings.provSel != 0 {
		t.Errorf("typing `j`/`k` moved the provider cursor to %d — those characters "+
			"belong in the key", m.settings.provSel)
	}
	if m.settings.provInput != "jkq" {
		t.Errorf("provInput = %q, want %q — the characters were eaten as navigation",
			m.settings.provInput, "jkq")
	}
}

// A NUL from a Windows paste must not enter an API key, and a non-ASCII rune
// must not be dropped from one.
func TestProvidersPageKeyFieldRejectsNulKeepsWideRunes(t *testing.T) {
	m := provModel(nil, "anthropic")
	for _, r := range []rune{0, 's', 'k', '-', 'é'} {
		out, _ := m.settingsProvidersKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(tuiModel)
	}
	got := m.settings.provInput
	if strings.ContainsRune(got, 0) {
		t.Errorf("a NUL entered the API key: %q", got)
	}
	if got != "sk-é" {
		t.Errorf("provInput = %q, want %q — a dropped character stores a credential "+
			"that is wrong and looks stored", got, "sk-é")
	}
}

// esc backs out of the page rather than closing the whole overlay, and a first
// esc clears a half-typed key instead of discarding the page.
func TestProvidersPageEscBacksOutInTwoSteps(t *testing.T) {
	m := provModel(nil, "anthropic")
	m.settings.provInput = "half-typed"
	out, _ := m.settingsProvidersKey(key("esc"))
	m = out.(tuiModel)
	if m.settings == nil || m.settings.page != settingsProviders {
		t.Fatal("the first esc left the page while a key was half-typed")
	}
	if m.settings.provInput != "" {
		t.Errorf("the first esc did not clear the field: %q", m.settings.provInput)
	}
	out, _ = m.settingsProvidersKey(key("esc"))
	m = out.(tuiModel)
	if m.settings == nil {
		t.Fatal("esc closed the settings overlay instead of returning to the top page")
	}
	if m.settings.page != settingsTop {
		t.Errorf("page = %v, want the top settings page", m.settings.page)
	}
}

// Enter with an empty field must not send a blank key to the daemon.
func TestProvidersPageRefusesAnEmptyKey(t *testing.T) {
	m := provModel(nil, "anthropic")
	out, cmd := m.settingsProvidersKey(key("enter"))
	m = out.(tuiModel)
	if cmd != nil {
		t.Error("Enter on an empty field issued a save — that stores a blank credential")
	}
	if m.settings.provErr == "" {
		t.Error("Enter on an empty field said nothing")
	}
}

// The candidate list is the union of "has a key" and "an agent could use it",
// and BOTH halves are load-bearing.
func TestUnionProvidersNeedsBothHalves(t *testing.T) {
	types := []api.AgentTypeCapability{
		{Type: "openclaw", SupportedProviders: []string{"anthropic", "openai", "google"}},
		{Type: "aider", SupportedProviders: []string{"local", "openai"}},
	}

	// A FRESH daemon: nothing stored. Without the agent types the page would be
	// empty and there would be no way to add the first key — the dead end this
	// page exists to remove.
	got := unionProviders(nil, types)
	if len(got) != 4 {
		t.Errorf("fresh daemon offers %v, want all four agent-declared providers", got)
	}

	// A provider configured by hand that NO agent type declares must not vanish
	// from the list just because nothing names it.
	got = unionProviders([]providercreds.Meta{{Name: "zzz-custom", KeySet: true}}, types)
	found := false
	for _, p := range got {
		if p == "zzz-custom" {
			found = true
		}
	}
	if !found {
		t.Errorf("a stored provider disappeared from the list: %v", got)
	}

	// Sorted and de-duplicated: openai is in both halves and both agent types.
	got = unionProviders([]providercreds.Meta{{Name: "openai"}}, types)
	n := 0
	for _, p := range got {
		if p == "openai" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("openai appears %d times in %v, want once", n, got)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("candidates are not sorted: %v — the cursor index has to be stable", got)
	}
}
