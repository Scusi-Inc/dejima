package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// agentTypeOption is one selectable agent type in the picker. Headless types
// also collect a command line before the picker resolves; the interactive ones
// (claude-code, codex) own a tmux session and are attachable.
//
// One island can hold several agents sharing its creds and git, so agent choice
// is a first-class step both when creating an island and when adding to one.
type agentTypeOption struct {
	typ      string
	desc     string
	headless bool
}

var agentTypeOptions = []agentTypeOption{
	{typ: "shell", desc: "plain terminal — a bash shell on the island workspace; attach and type"},
	{typ: "claude-code", desc: "interactive AI agent — attach and drive it"},
	{typ: "codex", desc: "interactive AI agent — attach and drive it"},
	{typ: "openclaw", desc: "OpenClaw assistant — a contained 24/7 brain (configure it in the workspace)"},
	{typ: api.AgentHeadless, desc: "background command — supervised, restarts on crash, logs only", headless: true},
}

// agentPickerPhase tracks the picker's internal step.
type agentPickerPhase int

const (
	pickType agentPickerPhase = iota // choosing a type
	pickCmd                          // typing a headless command
)

// agentPicker is a reusable two-step chooser: pick a type, and — for headless —
// type the command it should run. It is embedded both in the new-island creator
// (tui_create.go) and the standalone add-agent overlay below; the host drives it
// via handleKey and reads the result with typ()/cmd().
type agentPicker struct {
	phase    agentPickerPhase
	cursor   int
	cmdInput string
}

func newAgentPicker() agentPicker { return agentPicker{} }

func (p agentPicker) selected() agentTypeOption { return agentTypeOptions[p.cursor] }
func (p agentPicker) typ() string               { return p.selected().typ }
func (p agentPicker) cmd() string               { return strings.TrimSpace(p.cmdInput) }

// pickerResult is what a key event did to the picker.
type pickerResult int

const (
	pickerOngoing pickerResult = iota // still picking
	pickerDone                        // a complete (type[, cmd]) selection
	pickerBack                        // user backed out (esc at the type step)
)

// handleKey advances the picker by one key. The caller acts on the result:
// pickerDone → read typ()/cmd(); pickerBack → close or return to the prior step.
func (p *agentPicker) handleKey(msg tea.KeyMsg) pickerResult {
	switch p.phase {
	case pickType:
		switch msg.String() {
		case "esc":
			return pickerBack
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor < len(agentTypeOptions)-1 {
				p.cursor++
			}
		case "enter":
			if p.selected().headless {
				p.phase = pickCmd
				return pickerOngoing
			}
			return pickerDone
		}
	case pickCmd:
		switch msg.String() {
		case "esc":
			p.phase = pickType
		case "enter":
			if p.cmd() != "" {
				return pickerDone
			}
		case "backspace":
			if p.cmdInput != "" {
				p.cmdInput = p.cmdInput[:len(p.cmdInput)-1]
			}
		default:
			if len(msg.String()) == 1 {
				p.cmdInput += msg.String()
			}
		}
	}
	return pickerOngoing
}

// view renders the picker into b under the given section title.
func (p agentPicker) view(b *strings.Builder, title string) {
	b.WriteString(styleHeader.Render(title))
	b.WriteString("\n")
	if p.phase == pickCmd {
		b.WriteString(styleMuted.Render("Headless: runs this command detached, captures its output to a per-agent\nlog, and restarts it on exit. Not attachable — watch it with logs."))
		b.WriteString("\n\n")
		b.WriteString("command: " + styleAccent.Render(p.cmdInput+"_"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] add   [esc] back to type"))
		return
	}
	for i, opt := range agentTypeOptions {
		line := fmt.Sprintf("%-12s %s", opt.typ, styleMuted.Render(opt.desc))
		if i == p.cursor {
			b.WriteString(styleSelected.Render("▶ " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + styleMuted.Render("[↑/↓] move   [⏎] continue   [esc] back"))
}

// ---------------------------------------------------------------------------
// Standalone "add an agent to this island" overlay
// ---------------------------------------------------------------------------

// agentAdderPhase tracks the standalone add flow: pick a type (+ headless cmd)
// via the shared picker, then an optional label, then the request is in flight.
type agentAdderPhase int

const (
	adderPick  agentAdderPhase = iota // choosing type via the picker
	adderLabel                        // typing an optional label
)

// agentAdder wraps the shared picker with a target island, an optional label,
// and an in-flight / error state. Owned by tuiModel as a pointer (nil = inactive).
type agentAdder struct {
	island  string
	picker  agentPicker
	phase   agentAdderPhase
	label   string
	adding  bool
	err     string
	memWarn string // non-empty when the host is low on memory (OOM caution)
}

// memPressureWarning returns an amber caution when the Docker host is low on
// memory, so the operator thinks twice before adding another agent — especially
// a heavy one (openclaw npm-installs + runs a model). This is the guard for the
// OOM that killed OpenClaw *and* a sibling agent during dogfooding (#23): adding
// a process to an already-full host is what tips it over. "" when there's
// comfortable headroom or stats aren't available.
//
// On Docker Desktop / colima no per-container memory limit is set, so each
// island's reported MemoryLimitBytes is the VM's total RAM; comparing the
// across-island total (overview) against it gives real headroom.
func memPressureWarning(isl api.IslandInfo, ov *api.OverviewResponse) string {
	if isl.Stats == nil || isl.Stats.MemoryLimitBytes == 0 || ov == nil {
		return ""
	}
	vmTotal := isl.Stats.MemoryLimitBytes
	used := ov.MemoryUsageBytes // sum across running islands
	if used == 0 {
		used = isl.Stats.MemoryUsageBytes
	}
	pct := float64(used) / float64(vmTotal)
	if pct < 0.75 {
		return ""
	}
	return fmt.Sprintf("⚠ host memory is tight — %s of %s used across islands (%.0f%%). "+
		"A heavy agent (e.g. openclaw, which npm-installs and runs a model) may trigger an "+
		"out-of-memory kill that can take sibling agents down with it. Consider hibernating an "+
		"idle island first; otherwise proceed with care.",
		humanBytes(used), humanBytes(vmTotal), pct*100)
}

type agentAddedMsg struct {
	island     string
	agentID    string // the new agent's id, for opening it in a new tab
	agentLabel string // its user-set label (the model list hasn't refreshed yet)
	attachable bool   // interactive (connect) vs headless (logs)
	err        error
}

// openAgentAdder starts the add-agent flow for an island, unless the island is
// stopped (adding an agent execs into a live container).
func (m tuiModel) openAgentAdder(island string) (tea.Model, tea.Cmd) {
	if isl, ok := m.islandByName(island); ok && isl.Container != "running" {
		m.lastError = fmt.Sprintf("island %q is %s; `w` to wake it before adding an agent", island, isl.Container)
		return m, nil
	}
	m.agentAdder = &agentAdder{island: island, picker: newAgentPicker()}
	if isl, ok := m.islandByName(island); ok {
		m.agentAdder.memWarn = memPressureWarning(isl, m.overview)
	}
	return m, nil
}

func (m tuiModel) agentAdderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := m.agentAdder
	if a.adding {
		return m, nil // ignore input while the request is in flight
	}
	if msg.String() == "ctrl+c" {
		m.agentAdder = nil
		return m, nil
	}
	if a.phase == adderLabel {
		switch msg.String() {
		case "esc":
			a.phase = adderPick // back to type selection
		case "enter":
			a.adding, a.err = true, ""
			return m, m.addAgentSpecCmd(a.island, api.AgentSpecRequest{
				Type: a.picker.typ(), Cmd: a.picker.cmd(), Label: strings.TrimSpace(a.label)})
		case "backspace":
			if a.label != "" {
				a.label = a.label[:len(a.label)-1]
			}
		default:
			if len(msg.String()) == 1 {
				a.label += msg.String()
			}
		}
		return m, nil
	}
	switch a.picker.handleKey(msg) {
	case pickerBack:
		m.agentAdder = nil
	case pickerDone:
		a.phase = adderLabel // type chosen → ask for an optional label
	}
	return m, nil
}

// addAgentSpecCmd posts a new agent to an island and reports the outcome.
func (m tuiModel) addAgentSpecCmd(name string, req api.AgentSpecRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ag, err := m.client.AddAgent(ctx, name, req)
		if err != nil {
			return agentAddedMsg{island: name, err: err}
		}
		return agentAddedMsg{
			island:     name,
			agentID:    ag.ID,
			agentLabel: ag.Label,
			attachable: ag.Attachable,
		}
	}
}

func (a *agentAdder) view() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Add agent — " + a.island))
	b.WriteString("\n\n")
	if a.memWarn != "" {
		b.WriteString(styleWaiting.Render(a.memWarn))
		b.WriteString("\n\n")
	}
	if a.adding {
		b.WriteString(styleAccent.Render("adding " + a.picker.typ() + "…"))
		return b.String()
	}
	if a.phase == adderLabel {
		b.WriteString(styleHeader.Render("Label"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("Optional display name for the " + a.picker.typ() + " agent (e.g. \"frontend\").\nLeave blank to use its id."))
		b.WriteString("\n\n")
		b.WriteString("label: " + styleAccent.Render(a.label+"_"))
		b.WriteString("\n\n" + styleMuted.Render("[⏎] add   [esc] back to type"))
	} else {
		a.picker.view(&b, "Agent type")
	}
	if a.err != "" {
		b.WriteString("\n\n" + styleErrored.Render("✗ "+a.err))
	}
	return b.String()
}
