package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

func TestMemPressureWarning(t *testing.T) {
	const vm = uint64(8) << 30 // 8 GiB VM
	isl := func(limit, usage uint64) api.IslandInfo {
		return api.IslandInfo{Stats: &api.IslandStats{MemoryLimitBytes: limit, MemoryUsageBytes: usage}}
	}
	ov := func(total uint64) *api.OverviewResponse {
		return &api.OverviewResponse{MemoryUsageBytes: total}
	}

	// Comfortable headroom (25%) → no warning.
	if w := memPressureWarning(isl(vm, 1<<30), ov(2<<30)); w != "" {
		t.Errorf("expected no warning at 25%% usage, got %q", w)
	}
	// Tight (87.5%) → warns.
	if w := memPressureWarning(isl(vm, 1<<30), ov(7<<30)); w == "" {
		t.Error("expected a warning at ~88% usage, got none")
	}
	// No stats / no overview → no warning (never cry wolf without data).
	if w := memPressureWarning(api.IslandInfo{}, ov(7<<30)); w != "" {
		t.Errorf("expected no warning without stats, got %q", w)
	}
	if w := memPressureWarning(isl(vm, 7<<30), nil); w != "" {
		t.Errorf("expected no warning without overview, got %q", w)
	}
}

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

// downsTo is the navigation that reaches the option for typ, DERIVED from
// agentTypeOptions rather than counted by hand.
//
// Every test below used to walk a hardcoded number of "down"s with the list
// order copied into a comment ("openclaw (index 3)", "5th: shell, claude-code,
// codex, openclaw, headless"). Those comments are scar tissue — the list has
// already been reordered twice, once to put the terminal in front and once to
// insert openclaw — and each reorder rewrote them by hand.
//
// The reason to stop doing that is not tidiness. A reorder does not FAIL a
// hardcoded walk; it points it at a DIFFERENT option. The assertion still runs,
// against something nobody meant to test, and a picker test that passes while
// selecting the wrong agent is worth less than no test.
//
// The lookup is the control on the helper itself: a typ that is not in the list
// is a test naming an option that no longer exists, which must stop the test
// rather than silently resolve to index 0.
func downsTo(t *testing.T, typ string) []string {
	t.Helper()
	for i, o := range agentTypeOptions {
		if o.typ == typ {
			keys := make([]string, i)
			for k := range keys {
				keys[k] = "down"
			}
			return keys
		}
	}
	t.Fatalf("no %q in agentTypeOptions — the picker no longer offers what this test names", typ)
	return nil
}

// pickerOn returns a picker sitting on typ, ready for the key that selects it.
func pickerOn(t *testing.T, typ string) agentPicker {
	t.Helper()
	p := newAgentPicker()
	feed(&p, downsTo(t, typ)...)
	return p
}

// An interactive AI type resolves on Enter with no command.
func TestAgentPickerInteractive(t *testing.T) {
	p := pickerOn(t, "claude-code")
	if r := feed(&p, "enter"); r != pickerDone {
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
	p := pickerOn(t, "shell")
	if r := feed(&p, "enter"); r != pickerDone {
		t.Fatalf("shell enter = %v, want pickerDone", r)
	}
	if p.typ() != "shell" {
		t.Errorf("typ = %q, want shell", p.typ())
	}
	if p.cmd() != "" {
		t.Errorf("cmd = %q, want empty for shell", p.cmd())
	}
}

// OpenClaw is a baked-launch headless type — picked like an interactive option
// (no command step), resolving on Enter.
func TestAgentPickerOpenclaw(t *testing.T) {
	p := pickerOn(t, "openclaw")
	if r := feed(&p, "enter"); r != pickerDone {
		t.Fatalf("openclaw enter = %v, want pickerDone", r)
	}
	if p.typ() != "openclaw" {
		t.Errorf("typ = %q, want openclaw", p.typ())
	}
}

// Selecting headless requires a command before the picker resolves.
func TestAgentPickerHeadlessNeedsCmd(t *testing.T) {
	p := pickerOn(t, api.AgentHeadless)
	// Enter on headless → command step, not a resolve.
	if r := feed(&p, "enter"); r != pickerOngoing {
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
	p := pickerOn(t, api.AgentHeadless)
	feed(&p, "enter") // into the headless command step
	if r := p.handleKey(key("esc")); r != pickerOngoing || p.phase != pickType {
		t.Fatalf("esc on cmd step: result=%v phase=%v, want ongoing/pickType", r, p.phase)
	}
	if r := p.handleKey(key("esc")); r != pickerBack {
		t.Fatalf("esc on type step = %v, want pickerBack", r)
	}
}
