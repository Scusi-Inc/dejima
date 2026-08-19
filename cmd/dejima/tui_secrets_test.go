package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/secrets"
)

// feedSecrets replays keystrokes into the secrets pane's key handler.
func feedSecrets(m tuiModel, keys ...string) tuiModel {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		out, _ := m.secretsKey(msg)
		m = out.(tuiModel)
	}
	return m
}

// The add flow must live IN the pane, not in a spawned window — the old window
// path ran `dejima secret set <island>` with one arg against a two-arg command
// and crashed with "accepts 2 arg(s), received 1". [a] now opens a two-step
// inline form: type NAME, then a masked VALUE.
func TestSecretsInlineAddFlow(t *testing.T) {
	m := tuiModel{secretsPane: &secretsView{island: "wildfire"}}

	m = feedSecrets(m, "a")
	if !m.secretsPane.adding || m.secretsPane.addPhase != 0 {
		t.Fatalf("after [a]: adding=%v phase=%d, want adding at the name step", m.secretsPane.adding, m.secretsPane.addPhase)
	}

	m = feedSecrets(m, "E", "X", "P", "O")
	if m.secretsPane.nameInput != "EXPO" {
		t.Fatalf("name input = %q, want EXPO", m.secretsPane.nameInput)
	}

	m = feedSecrets(m, "enter")
	if m.secretsPane.addPhase != 1 {
		t.Fatalf("after name Enter: phase=%d, want the value step", m.secretsPane.addPhase)
	}

	// The value must NOT echo — the view masks it to bullets of its length.
	m = feedSecrets(m, "s", "3", "c", "r", "e", "t")
	body := m.secretsPane.view(80)
	if strings.Contains(body, "s3cret") {
		t.Errorf("the value is visible in the rendered pane:\n%s", body)
	}
	if !strings.Contains(body, strings.Repeat("•", 6)) {
		t.Errorf("value should render as 6 bullets:\n%s", body)
	}
}

// A reserved name is rejected at the NAME step, before the operator types a
// value — the error should come early, not after the whole flow.
func TestSecretsInlineAddRejectsReservedName(t *testing.T) {
	m := tuiModel{secretsPane: &secretsView{island: "wildfire"}}
	m = feedSecrets(m, "a", "P", "A", "T", "H", "enter")

	if m.secretsPane.addPhase != 0 {
		t.Errorf("a reserved name advanced past the name step: phase=%d", m.secretsPane.addPhase)
	}
	if !strings.Contains(m.secretsPane.err, "reserved") {
		t.Errorf("expected a reserved-name error; got %q", m.secretsPane.err)
	}
}

// esc backs out of the add form without leaving a half-typed value behind.
func TestSecretsInlineAddCancel(t *testing.T) {
	m := tuiModel{secretsPane: &secretsView{island: "wildfire"}}
	m = feedSecrets(m, "a", "N", "A", "M", "E", "enter", "s", "e", "c", "esc")
	if m.secretsPane.adding {
		t.Error("esc should exit the add form")
	}
	if m.secretsPane.valInput != "" || m.secretsPane.nameInput != "" {
		t.Errorf("esc left inputs behind: name=%q val=%q", m.secretsPane.nameInput, m.secretsPane.valInput)
	}
}

// [x] removes the SELECTED row, not always the first — the cursor moves with
// up/down and the confirm targets whatever it lands on.
func TestSecretsRemoveTargetsCursor(t *testing.T) {
	m := tuiModel{secretsPane: &secretsView{
		island:  "wildfire",
		secrets: []secrets.Meta{{Name: "A"}, {Name: "B"}, {Name: "C"}},
	}}
	m = feedSecrets(m, "down", "down") // cursor → C
	if m.secretsPane.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.secretsPane.cursor)
	}
	out, _ := m.secretsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = out.(tuiModel)
	if m.confirm == nil || m.confirm.agent != "C" {
		t.Fatalf("remove confirm targets %v, want the selected row C", m.confirm)
	}
}

// TestSecretsRestartOpensChecklist: [R] in the Secrets pane opens the
// "which agents to restart" checklist and closes the pane. Note what this no
// longer claims: a restart does not load a newly-added secret — see
// TestSecretsPaneLeadsWithRecreate for why.
func TestSecretsRestartOpensChecklist(t *testing.T) {
	m := tuiModel{secretsPane: &secretsView{island: "wildfire", restartPending: true}}
	m = feedSecrets(m, "R")
	if m.secretsPane != nil {
		t.Errorf("[R] should close the secrets pane; got %+v", m.secretsPane)
	}
	if m.restartPane == nil || m.restartPane.island != "wildfire" {
		t.Fatalf("[R] should open the restart checklist for the island; got %+v", m.restartPane)
	}
}

// Two container generations exist and the pane has to be true for both.
//
// An island created BEFORE the secrets-mount fix — which is every island running
// today — has the old single-FILE bind. A file bind holds the inode; the daemon
// writes with CreateTemp+Rename, which installs a new one, so that container
// reads the file it started with for its whole life however many times a process
// inside it restarts. Only a recreate re-resolves. An island created after it has
// the DIRECTORY bound, and a restart does apply.
//
// So the pane leads with RECREATE. Recreate is correct for both generations — it
// always applies — which makes leading with it never wrong, only sometimes
// heavier than needed. Leading with restart is wrong today for every island in
// existence, and it fails the expensive way: the operator acts, gets no error,
// and believes a credential was applied or revoked when it wasn't.
//
// The condition that will relax this is stated IN the copy, not just here, so it
// cannot quietly go stale once islands have been recreated on the new mount.
func TestSecretsPaneLeadsWithRecreate(t *testing.T) {
	m := tuiModel{secretsPane: &secretsView{island: "wildfire", restartPending: true}}
	out := m.secretsPane.view(100)
	if !strings.Contains(out, "RECREATE THE ISLAND TO APPLY") {
		t.Errorf("pending banner must lead with recreate; got %q", out)
	}
	if !strings.Contains(out, "Restarting an agent does NOT change that") {
		t.Errorf("pending banner must say a restart is not the remedy; got %q", out)
	}
	// The condition, in the operator's view rather than only in a commit message.
	if !strings.Contains(out, "already recreated since the secrets-mount fix") {
		t.Errorf("banner must state when a restart WOULD be enough, so it can't silently go stale; got %q", out)
	}

	// And the remedy that works has to be reachable where it is read — it used to
	// be [R] then [!], i.e. three keystrokes behind the one that doesn't work.
	got := feedSecrets(m, "!")
	if got.secretsPane != nil {
		t.Errorf("[!] should close the secrets pane; got %+v", got.secretsPane)
	}
	if got.confirm == nil || got.confirm.verb != "recreate-island" || got.confirm.island != "wildfire" {
		t.Fatalf("[!] should arm recreate-island for the island in one key; got %+v", got.confirm)
	}
}

// The checklist must not repeat the promise the pane just retracted. It used to
// say it relaunches agents "so they pick up new secrets" — true-sounding, and
// wrong for the case an operator arrives from.
func TestRestartChecklistDoesNotPromiseSecrets(t *testing.T) {
	m := seededModel(t, island("alpha", "a1")).openRestartView("alpha")
	out := m.restartPane.view(100)
	if !strings.Contains(out, "needs [!] recreate, not a restart") {
		t.Errorf("checklist must retract the secrets promise; got %q", out)
	}
	if strings.Contains(out, "so they pick up new secrets") {
		t.Errorf("checklist still promises a restart applies secrets; got %q", out)
	}
}
