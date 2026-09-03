package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

func importModel(scopes ...api.PortScopeView) tuiModel {
	return tuiModel{importPane: &importView{
		island: "isl", scopes: scopes, recursive: true, step: importPickScope,
	}}
}

func feedImport(m tuiModel, keys ...string) tuiModel {
	for _, k := range keys {
		next, _ := m.importKey(key(k))
		m = next.(tuiModel)
	}
	return m
}

// The async messages must be ROUTED, not merely handled. A pane whose load
// message never reaches Update sits on "⏳ loading…" forever — it compiles, it
// renders, and it is completely broken. I wired the handlers before the routing
// and this is the test that catches that gap.
func TestImport_ScopesMessageIsRouted(t *testing.T) {
	m := tuiModel{importPane: &importView{island: "isl", loading: true}}
	next, _ := m.Update(importScopesMsg{island: "isl", scopes: []api.PortScopeView{{Name: "vault"}}})
	got := next.(tuiModel)

	if got.importPane.loading {
		t.Fatal("still loading after the scopes arrived — the message isn't routed through Update")
	}
	if len(got.importPane.scopes) != 1 {
		t.Errorf("scopes = %+v, want the one that arrived", got.importPane.scopes)
	}
}

func TestImport_ResultMessageIsRouted(t *testing.T) {
	m := tuiModel{importPane: &importView{island: "isl", step: importRunning}}
	next, _ := m.Update(importDoneMsg{res: &api.PortIntakeResponse{Recursive: true, BatchID: "abc"}})
	got := next.(tuiModel)

	if got.importPane.step != importDone {
		t.Fatal("the pane never left importRunning — the result message isn't routed")
	}
	if got.importPane.result == nil {
		t.Error("the result was dropped")
	}
}

// A path is required. Sending an empty one would import the scope root by
// accident on a keystroke, which is the largest possible import from the
// smallest possible mistake.
func TestImport_EmptyPathIsRefused(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m = feedImport(m, "enter") // choose the scope
	if m.importPane.step != importTypePath {
		t.Fatalf("step = %v, want importTypePath", m.importPane.step)
	}
	m = feedImport(m, "enter") // submit an empty path
	if m.importPane.step != importTypePath {
		t.Fatal("an empty path advanced — it would import the whole scope root")
	}
	if m.importPane.err == "" {
		t.Error("refused silently; the user sees nothing happen and presses it again")
	}
}

// Recursion is an explicit toggle, not an inference from whether the path
// happens to name a directory. One file and a whole tree differ by orders of
// magnitude, so the choice is visible and deliberate.
func TestImport_RecursiveIsAnExplicitToggle(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m = feedImport(m, "enter")
	if !m.importPane.recursive {
		t.Fatal("default should be folder import — that is what this pane is for")
	}
	m = feedImport(m, "tab")
	if m.importPane.recursive {
		t.Error("tab did not toggle recursion off")
	}
	m = feedImport(m, "tab")
	if !m.importPane.recursive {
		t.Error("tab did not toggle recursion back on")
	}
}

// Deny-all is the default, so an island with no scopes is the ordinary first-run
// state rather than an error — and the pane has to say what to do about it,
// since "no scopes" alone reads as a broken screen.
func TestImport_NoScopesExplainsHowToGrantOne(t *testing.T) {
	m := importModel() // no scopes
	out := m.renderImport()
	if !strings.Contains(out, "port grant") {
		t.Errorf("an island with no scopes must be told how to get one, got:\n%s", out)
	}
	// Enter must not crash or advance when there is nothing to choose.
	m = feedImport(m, "enter")
	if m.importPane.step != importPickScope {
		t.Error("advanced past scope selection with no scope selected")
	}
}

// A partial import leaves real files in place. If the pane does not say so, a
// reader assumes it was undone, imports again, and cannot explain the counts.
func TestImport_PartialResultSaysNothingWasRolledBack(t *testing.T) {
	m := tuiModel{importPane: &importView{
		island: "isl", step: importDone,
		result: &api.PortIntakeResponse{
			Recursive: true, BatchID: "abc", Bytes: 3,
			Files:   []api.PortIntakeFile{{Rel: "a.txt", Bytes: 3}},
			Failed:  []api.PortIntakeFailed{{Rel: "b.txt", Error: "permission denied"}},
			Skipped: []api.PortIntakeSkip{{Rel: "link", Reason: "symlink (never followed)"}},
		},
	}}
	out := m.renderImport()
	for _, want := range []string{"did NOT cross", "b.txt", "nothing was rolled back", "symlink"} {
		if !strings.Contains(out, want) {
			t.Errorf("result view is missing %q; got:\n%s", want, out)
		}
	}
}

// The failure path must render rather than panic on a nil result — an error
// response leaves result nil, and this view is what the user sees when the
// import failed outright.
func TestImport_ErrorResultRendersWithoutAResult(t *testing.T) {
	m := tuiModel{importPane: &importView{
		island: "isl", step: importDone, err: errors.New("scope not found").Error(),
	}}
	out := m.renderImport()
	if !strings.Contains(out, "scope not found") {
		t.Errorf("the error is not shown; got:\n%s", out)
	}
}

var _ tea.Model = tuiModel{}

// A refusal must offer the way out IN THE TUI. The operator's standing rule is
// that the TUI does not send people to the CLI, and a cap that can only be
// raised with a flag does exactly that.
func TestImportRefusalOffersRaisingTheCaps(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m.importPane.step, m.importPane.err = importDone, "refusing to import: 4213 files"
	view := m.renderImport()
	if !strings.Contains(view, "[c] raise the caps") {
		t.Fatalf("the refusal screen offers no way to raise the cap:\n%s", view)
	}
	m = feedImport(m, "c")
	if m.importPane.step != importCaps {
		t.Fatalf("step = %v, want importCaps", m.importPane.step)
	}
}

// Digits and size suffixes are the whole alphabet of these fields. Reusing the
// list's j/k/q handling would eat "1G" and close the pane mid-number — the bug
// the agent-adder key step already had to solve once.
func TestImportCapsFieldTakesDigitsNotCursorKeys(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m.importPane.step = importCaps
	m = feedImport(m, "5", "0", "0", "0")
	if m.importPane.maxFiles != "5000" {
		t.Errorf("maxFiles = %q, want 5000", m.importPane.maxFiles)
	}
	m = feedImport(m, "tab", "2", "G")
	if m.importPane.maxBytes != "2G" {
		t.Errorf("maxBytes = %q, want 2G", m.importPane.maxBytes)
	}
	if m.importPane.step != importCaps {
		t.Errorf("a typed character left the caps field (step = %v)", m.importPane.step)
	}
}

// An unparseable size must NOT run the import. Sending 0 would re-request the
// default caps and return the identical refusal, which reads as "the override
// does not work" rather than "you typed the size wrong".
func TestImportBadSizeDoesNotRunTheImport(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m.importPane.step, m.importPane.path = importCaps, "daily"
	m.importPane.maxBytes = "2 gigs"
	next, cmd := m.importKey(key("enter"))
	m = next.(tuiModel)
	if cmd != nil {
		t.Fatal("an unparseable size still launched the import")
	}
	if m.importPane.step != importCaps {
		t.Errorf("step = %v, want to stay on importCaps", m.importPane.step)
	}
	if m.importPane.capParseErr == "" {
		t.Error("no parse error was shown")
	}
	if !strings.Contains(m.renderImport(), "max size") {
		t.Error("the parse error is not rendered")
	}
}

func TestImportCapsRoundTripToTypePath(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m.importPane.step = importCaps
	m = feedImport(m, "esc")
	if m.importPane.step != importTypePath {
		t.Errorf("esc from caps went to %v, want importTypePath", m.importPane.step)
	}
	if m.importPane == nil {
		t.Fatal("esc closed the whole pane instead of going back")
	}
}

// A raised cap has to be VISIBLE on the screen that launches the import, or the
// operator cannot tell an override apart from the default.
func TestImportShowsRaisedCapsBeforeImporting(t *testing.T) {
	m := importModel(api.PortScopeView{Name: "vault"})
	m.importPane.step, m.importPane.path = importTypePath, "daily"
	m.importPane.maxFiles = "5000"
	view := m.renderImport()
	if !strings.Contains(view, "5000") || !strings.Contains(view, "caps raised") {
		t.Errorf("raised caps are invisible before the import runs:\n%s", view)
	}
	if !strings.Contains(view, "default") {
		t.Errorf("the un-raised cap does not say it is the default:\n%s", view)
	}
}
