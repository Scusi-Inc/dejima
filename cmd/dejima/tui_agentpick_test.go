package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func feed(p *agentPicker, keys ...string) pickerResult {
	var r pickerResult
	for _, k := range keys {
		r = p.handleKey(key(k))
	}
	return r
}

// An interactive AI type resolves on Enter with no command (claude-code is the
// second option now that the terminal leads the list).
func TestAgentPickerInteractive(t *testing.T) {
	p := newAgentPicker()
	if r := feed(&p, "down", "enter"); r != pickerDone {
		t.Fatalf("interactive enter = %v, want pickerDone", r)
	}
	if p.typ() != "claude-code" {
		t.Errorf("typ = %q, want claude-code", p.typ())
	}
	if p.cmd() != "" {
		t.Errorf("cmd = %q, want empty for interactive", p.cmd())
	}
}

// The shell (terminal) type is interactive — resolves on Enter with no command.
func TestAgentPickerShell(t *testing.T) {
	p := newAgentPicker()
	if r := feed(&p, "enter"); r != pickerDone { // shell leads the list (index 0)
		t.Fatalf("shell enter = %v, want pickerDone", r)
	}
	if p.typ() != "shell" {
		t.Errorf("typ = %q, want shell", p.typ())
	}
	if p.cmd() != "" {
		t.Errorf("cmd = %q, want empty for shell", p.cmd())
	}
}

// Selecting headless requires a command before the picker resolves.
func TestAgentPickerHeadlessNeedsCmd(t *testing.T) {
	p := newAgentPicker()
	// Move to the headless option (fourth: claude-code, codex, shell, headless),
	// then Enter → command step.
	if r := feed(&p, "down", "down", "down", "enter"); r != pickerOngoing {
		t.Fatalf("headless enter = %v, want pickerOngoing (awaiting cmd)", r)
	}
	if p.typ() != api.AgentHeadless {
		t.Fatalf("typ = %q, want headless", p.typ())
	}
	// Enter with an empty command does not resolve.
	if r := p.handleKey(key("enter")); r != pickerOngoing {
		t.Fatalf("empty cmd enter = %v, want pickerOngoing", r)
	}
	// Type a command, then Enter resolves.
	if r := feed(&p, "p", "y"); r != pickerOngoing {
		t.Fatalf("typing = %v, want pickerOngoing", r)
	}
	if r := p.handleKey(key("enter")); r != pickerDone {
		t.Fatalf("cmd enter = %v, want pickerDone", r)
	}
	if p.cmd() != "py" {
		t.Errorf("cmd = %q, want py", p.cmd())
	}
}

// After a type is chosen the adder asks for an optional label; esc returns to
// type selection.
func TestAgentAdderLabelStep(t *testing.T) {
	m := initialTUIModel(nil)
	a := &agentAdder{island: "myrepo", picker: newAgentPicker()}
	m.agentAdder = a

	m.agentAdderKey(key("enter")) // pick the first option (shell) → label step
	if a.phase != adderLabel {
		t.Fatalf("after type pick: phase = %v, want adderLabel", a.phase)
	}
	for _, c := range []string{"a", "p", "i"} {
		m.agentAdderKey(key(c))
	}
	if a.label != "api" {
		t.Errorf("label = %q, want api", a.label)
	}
	m.agentAdderKey(key("esc")) // back to type selection
	if a.phase != adderPick {
		t.Errorf("esc on label step: phase = %v, want adderPick", a.phase)
	}
}

// Esc on the command step returns to type selection (not a cancel); esc on the
// type step backs out.
func TestAgentPickerEscape(t *testing.T) {
	p := newAgentPicker()
	feed(&p, "down", "down", "down", "enter") // into the headless command step
	if r := p.handleKey(key("esc")); r != pickerOngoing || p.phase != pickType {
		t.Fatalf("esc on cmd step: result=%v phase=%v, want ongoing/pickType", r, p.phase)
	}
	if r := p.handleKey(key("esc")); r != pickerBack {
		t.Fatalf("esc on type step = %v, want pickerBack", r)
	}
}
