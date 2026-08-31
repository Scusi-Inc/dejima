package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runes builds the key event a terminal delivers for typed or pasted text.
func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// Every text field in the TUI used `len(msg.String()) == 1` to mean "this is a
// printable character". That asks how many BYTES the key rendered to, which is
// not the question, and it is wrong in both directions:
//
//	NUL passes      len("\x00") is 1. A Windows paste arrives character-by-
//	                character through ConPTY with a leading NUL, so an operator
//	                pasting a secret name got
//	                  ✗ invalid secret name "\x00TAURI_SIGNING_PRIVATE_KEY_PASSWORD"
//	                naming a character they never typed. Typing the same name
//	                worked, which is what made it look like a validation bug
//	                rather than an input bug.
//
//	é fails         two bytes, so it is dropped IN SILENCE. That is the worse
//	                half and nobody reported it: an API key or passphrase with a
//	                non-ASCII character stores wrong, reports success, and fails
//	                later somewhere with no connection to this screen.
func TestSecretInputRejectsControlCharsAndKeepsWideRunes(t *testing.T) {
	base := func() tuiModel {
		return tuiModel{secretsPane: &secretsView{island: "isl", adding: true, addPhase: 0}}
	}

	t.Run("a leading NUL from a Windows paste never reaches the field", func(t *testing.T) {
		m := base()
		for _, k := range []tea.KeyMsg{runes("\x00"), runes("T"), runes("A"), runes("U")} {
			out, _ := m.secretsAddKey(k)
			m = out.(tuiModel)
		}
		if got := m.secretsPane.nameInput; got != "TAU" {
			t.Errorf("nameInput = %q, want %q — the NUL was accepted as a printable "+
				"character and the operator saw it quoted back in a validation error", got, "TAU")
		}
	})

	t.Run("a non-ASCII rune is kept, not silently dropped", func(t *testing.T) {
		m := base()
		m.secretsPane.addPhase = 1 // the VALUE field: a passphrase
		for _, k := range []tea.KeyMsg{runes("p"), runes("é"), runes("1")} {
			out, _ := m.secretsAddKey(k)
			m = out.(tuiModel)
		}
		if got := m.secretsPane.valInput; got != "pé1" {
			t.Errorf("valInput = %q, want %q — a passphrase character was dropped without "+
				"a word, so the secret stores wrong and reports success", got, "pé1")
		}
	})

	t.Run("a bracketed paste arrives whole", func(t *testing.T) {
		m := base()
		out, _ := m.secretsAddKey(runes("MY_TOKEN"))
		m = out.(tuiModel)
		if got := m.secretsPane.nameInput; got != "MY_TOKEN" {
			t.Errorf("nameInput = %q, want the whole pasted string — a multi-rune paste "+
				"failed the byte test and was dropped entirely", got)
		}
	})
}

// The same line was copy-pasted into the agent-key and create-wizard fields.
// Those take API KEYS, where the silent half is the dangerous one.
func TestApiKeyFieldsKeepEveryPastedRune(t *testing.T) {
	m := tuiModel{agentAdder: &agentAdder{phase: adderKey, keyProviders: []string{"anthropic"}}}
	for _, k := range []tea.KeyMsg{runes("\x00"), runes("sk-"), runes("é"), runes("9")} {
		out, _ := m.agentAdderKey(k)
		m = out.(tuiModel)
	}
	got := m.agentAdder.keyInput
	if strings.ContainsRune(got, 0) {
		t.Errorf("keyInput = %q — a control character was stored in an API key", got)
	}
	if !strings.Contains(got, "é") {
		t.Errorf("keyInput = %q, want the non-ASCII rune kept — a dropped character "+
			"stores a credential that is wrong and looks stored", got)
	}
}
