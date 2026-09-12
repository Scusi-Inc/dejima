package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/localmodel"
)

// The header's server line trades two hints that never changed what you'd do
// next ([I] team, the ssh address) for one that does: whether this server can
// run an agent you don't pay per token for.
//
// Each test below has a SUBJECT — a header actually rendered from a model in a
// stated state — rather than asserting on the note helper alone, because the
// way this regresses is the helper staying right while nothing calls it.

func TestHeaderNamesTheOneModelThatIsPulled(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.localModels = []string{"qwen-coder-7b"}
	if out := m.renderHeader(); !strings.Contains(out, "local: qwen-coder-7b") {
		t.Errorf("header should name the single pulled model:\n%s", out)
	}
}

func TestHeaderCountsSeveralModelsInsteadOfListingThem(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.localModels = []string{"qwen-coder-7b", "llama3.1-8b", "mistral-7b"}
	out := m.renderHeader()
	if !strings.Contains(out, "local: 3 models") {
		t.Errorf("header should count, not list:\n%s", out)
	}
	if strings.Contains(out, "llama3.1-8b") {
		t.Errorf("header listed model names instead of counting them:\n%s", out)
	}
}

// Nothing pulled says nothing. A standing "local: none" is noise on every
// redraw of every session for a fleet that will never use local models.
func TestHeaderIsSilentWhenNothingIsPulled(t *testing.T) {
	m := seededModel(t, island("alpha"))
	if out := m.renderHeader(); strings.Contains(out, "local:") {
		t.Errorf("header should say nothing about local models when none are pulled:\n%s", out)
	}
}

// The move, not the deletion: the ssh address is off the header and ON the
// settings page. Asserting only the first half would pass just as well if the
// address had been dropped on the floor.
func TestSSHAddressMovedFromHeaderToSettings(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.overview = &api.OverviewResponse{SSHAddr: "minion:2222"}

	if out := m.renderHeader(); strings.Contains(out, "minion:2222") {
		t.Errorf("the ssh address is a copy-once value; it should be off the header:\n%s", out)
	}
	if out := m.renderHeader(); strings.Contains(out, "[I]") {
		t.Errorf("the team hint moved to Settings → Team & invites:\n%s", out)
	}

	s := m.openSettings()
	out := s.renderSettings()
	if !strings.Contains(out, "SSH access") {
		t.Errorf("no SSH access row on the preferences page:\n%s", out)
	}
	if !strings.Contains(out, "minion:2222") {
		t.Errorf("the SSH access row should carry the address it took off the header:\n%s", out)
	}
	if !strings.Contains(out, "Team & invites") {
		t.Errorf("no Team & invites row to have moved [I] to:\n%s", out)
	}
}

// A façade that is off says so. A blank row cannot be told apart from a row
// that failed to load.
func TestSSHAccessRowStatesWhenTheFacadeIsOff(t *testing.T) {
	m := seededModel(t, island("alpha")).openSettings()
	out := m.renderSettings()
	if !strings.Contains(out, "SSH access") || !strings.Contains(out, "--ssh") {
		t.Errorf("the SSH access row should state that the façade is off:\n%s", out)
	}
}

// The row has to DO something: reaching it and pressing enter arms the same
// account-wide setup the Server menu's [H] arms.
func TestSSHAccessRowArmsTheSetup(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.overview = &api.OverviewResponse{SSHAddr: "minion:2222"}
	m = m.openSettings()
	m.settings.sel = 10 // the SSH access row

	upd, _ := m.settingsKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := upd.(tuiModel)
	if mm.confirm == nil || mm.confirm.verb != "setup-ssh" {
		t.Fatalf("row 10 should arm setup-ssh, got %+v", mm.confirm)
	}
	if mm.settings != nil {
		t.Errorf("the overlay should close behind the confirm, got %+v", mm.settings)
	}
}

// The names the header shows are the ones the operator pulled by.
func TestPulledModelNamesPrefersTheCuratedAlias(t *testing.T) {
	got := pulledModelNames(&localmodel.Status{Models: []localmodel.InstalledModel{
		{Ref: "qwen2.5-coder:7b-instruct-q4_K_M", Alias: "qwen-coder-7b"},
		{Ref: "some-uncurated:latest"},
		{}, // neither — contributes no name rather than a blank one
	}})
	want := []string{"qwen-coder-7b", "some-uncurated:latest"}
	if len(got) != len(want) {
		t.Fatalf("pulledModelNames = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pulledModelNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := pulledModelNames(nil); got != nil {
		t.Errorf("no status is no names, got %q", got)
	}
}

// The header's copy is refreshed by the message itself, not by the settings
// page being open on it — otherwise a pull made from that page updates the
// sub-page and leaves the header showing the pre-pull list until a restart.
func TestLocalStatusRefreshesTheHeaderWithNoSettingsOverlay(t *testing.T) {
	m := seededModel(t, island("alpha"))
	upd, _ := m.Update(localStatusMsg{status: &localmodel.Status{
		Models: []localmodel.InstalledModel{{Ref: "r", Alias: "qwen-coder-7b"}},
	}})
	mm := upd.(tuiModel)
	if len(mm.localModels) != 1 || mm.localModels[0] != "qwen-coder-7b" {
		t.Fatalf("localModels = %q, want the pulled model", mm.localModels)
	}
	if out := mm.renderHeader(); !strings.Contains(out, "local: qwen-coder-7b") {
		t.Errorf("header did not pick up the refreshed status:\n%s", out)
	}
}

// A failed fetch must not erase what we know. "the daemon didn't answer" is
// not "nothing is pulled".
func TestFailedLocalStatusKeepsTheLastKnownModels(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.localModels = []string{"qwen-coder-7b"}
	upd, _ := m.Update(localStatusMsg{err: errFetch{}})
	if mm := upd.(tuiModel); len(mm.localModels) != 1 {
		t.Errorf("a failed fetch emptied the header note: %q", mm.localModels)
	}
}

type errFetch struct{}

func (errFetch) Error() string { return "dial: no daemon" }
