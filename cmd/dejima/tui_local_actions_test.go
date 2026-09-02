package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/localmodel"
)

// TestLocalActionsFollowStatus: the Local models page offers exactly the steps
// the host's current state allows. Setting up a local model used to mean
// reading a backtick-quoted command off a read-only page and retyping it in
// another terminal; the rows are that command, so what they offer has to track
// the status they were derived from.
func TestLocalActionsFollowStatus(t *testing.T) {
	top := localmodel.Model{Alias: "qwen-coder", Ref: "qwen2.5-coder:14b", Params: "14B"}

	// Nothing on the host: install is the only thing that can come next — a
	// "pull" row here would hand the operator a step that cannot work yet.
	acts := localActions(&localmodel.Status{Backend: "ollama"})
	if len(acts) != 1 || !strings.HasPrefix(acts[0].label, "Install ollama") {
		t.Fatalf("not-installed should offer install only, got %+v", acts)
	}
	if got := strings.Join(acts[0].args, " "); got != "local install" {
		t.Errorf("install row should run `dejima local install`, got %q", got)
	}

	// Installed, recommended model not pulled: pulling it is the next step and
	// leads the list; registration stays available underneath.
	acts = localActions(&localmodel.Status{
		Backend: "ollama", Installed: true, Running: true,
		Recommend: localmodel.Recommendation{Top: &top},
	})
	if len(acts) != 2 {
		t.Fatalf("installed + nothing pulled should offer pull and register, got %+v", acts)
	}
	if !strings.Contains(acts[0].label, "qwen-coder") || !strings.Contains(acts[0].label, "14B") {
		t.Errorf("pull row should name the model and its size, got %q", acts[0].label)
	}
	if got := strings.Join(acts[0].args, " "); got != "local pull qwen-coder" {
		t.Errorf("pull row should run `dejima local pull qwen-coder`, got %q", got)
	}

	// Already pulled — by ref, which is how the backend reports it — so the pull
	// row is gone. Offering it again would re-download several GB for nothing.
	acts = localActions(&localmodel.Status{
		Backend: "ollama", Installed: true, Running: true,
		Models:    []localmodel.InstalledModel{{Ref: "qwen2.5-coder:14b"}},
		Recommend: localmodel.Recommendation{Top: &top},
	})
	for _, a := range acts {
		if strings.HasPrefix(a.verb, "pull") {
			t.Errorf("a pulled model should not be offered for pull again, got %+v", acts)
		}
	}

	// No status yet (the page fetches async) means no rows — the cursor has
	// nothing to land on until the daemon answers.
	if acts := localActions(nil); acts != nil {
		t.Errorf("no status should mean no actions, got %+v", acts)
	}
}

// TestTUILocalPageRunsAction: ⏎ on a row hands the terminal to the matching
// `dejima local …` child, and the footer says so. The page was read-only, with
// esc as its only key — the whole flow it describes lived in another terminal.
func TestTUILocalPageRunsAction(t *testing.T) {
	// Open Settings (`,`) → Local models (8th row, index 7).
	m := driveKeys(t, seededModel(t, island("alpha")),
		",", "j", "j", "j", "j", "j", "j", "j", "enter")
	if m.settings == nil || m.settings.page != settingsLocal {
		t.Fatalf("expected the Local models sub-page, got %+v", m.settings)
	}

	// The daemon's answer lands (it never does in a test's socket-less client,
	// so deliver it by hand — this is the same message fetchLocalStatusCmd sends).
	mm, _ := m.Update(localStatusMsg{status: &localmodel.Status{Backend: "ollama", Endpoint: "http://host.docker.internal:11434/v1"}})
	m = mm.(tuiModel)
	if len(m.settings.localActs) != 1 {
		t.Fatalf("status should populate the rows, got %+v", m.settings.localActs)
	}

	view := m.renderSettings()
	for _, want := range []string{"Install ollama on the host", "⏎ run", "esc back"} {
		if !strings.Contains(view, want) {
			t.Errorf("Local models page should render %q; got:\n%s", want, view)
		}
	}

	// ⏎ returns a command — the ExecProcess that suspends the TUI and runs the
	// installer. A page that swallowed the key would look identical.
	if _, cmd := m.handleKey(key("enter")); cmd == nil {
		t.Error("⏎ on an action row should run it; got no command")
	}
}

// TestTUILocalPageClampsCursorOnRefresh: an action changes what the page can
// offer (installing removes its own row), and the refresh that follows must not
// leave the cursor pointing past the end of the shorter list.
func TestTUILocalPageClampsCursorOnRefresh(t *testing.T) {
	top := localmodel.Model{Alias: "qwen-coder", Ref: "qwen2.5-coder:14b", Params: "14B"}
	m := driveKeys(t, seededModel(t, island("alpha")),
		",", "j", "j", "j", "j", "j", "j", "j", "enter")

	mm, _ := m.Update(localStatusMsg{status: &localmodel.Status{
		Backend: "ollama", Installed: true, Running: true,
		Recommend: localmodel.Recommendation{Top: &top},
	}})
	m = mm.(tuiModel)
	m = driveKeys(t, m, "j") // onto the second row (register)
	if m.settings.sel != 1 {
		t.Fatalf("expected the cursor on row 1, got %d", m.settings.sel)
	}

	// Now the backend reads as gone (uninstalled, or the daemon swapped): one row.
	mm, _ = m.Update(localStatusMsg{status: &localmodel.Status{Backend: "ollama"}})
	m = mm.(tuiModel)
	if m.settings.sel != 0 {
		t.Errorf("cursor should snap back into range, got %d of %d rows",
			m.settings.sel, len(m.settings.localActs))
	}
}
