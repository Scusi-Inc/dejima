package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
)

func TestRenderGithubView(t *testing.T) {
	m := tuiModel{
		github: &githubView{
			identities: []githubid.Meta{{Name: "alice", Login: "alice-gh", Default: true}},
		},
		islands: []api.IslandInfo{
			{Name: "ok-isl"},
			{Name: "broken-isl", GitHubCredMissing: true},
		},
	}
	out := m.renderGithubView()
	for _, want := range []string{
		"alice",              // identity name
		"alice-gh",           // login
		"default",            // tag
		"broken-isl",         // the cred-missing island is badged
		"[c] connect GitHub", // the connect affordance
	} {
		if !strings.Contains(out, want) {
			t.Errorf("github pane missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "ok-isl") {
		t.Error("only cred-missing islands should be listed, not healthy ones")
	}
}

func TestRenderGithubViewEmpty(t *testing.T) {
	m := tuiModel{github: &githubView{}} // no identities, not loading
	if out := m.renderGithubView(); !strings.Contains(out, "No GitHub identity yet") {
		t.Errorf("empty pane should prompt to connect one\n%s", out)
	}
}

func TestGithubMissingCredIslands(t *testing.T) {
	m := tuiModel{islands: []api.IslandInfo{
		{Name: "a"},
		{Name: "b", GitHubCredMissing: true},
		{Name: "c", GitHubCredMissing: true},
	}}
	got := m.githubMissingCredIslands()
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("missing-cred islands = %v, want [b c]", got)
	}
}

func TestGithubKeyEscCloses(t *testing.T) {
	m := tuiModel{github: &githubView{}}
	updated, _ := m.githubKey(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(tuiModel).github != nil {
		t.Error("esc should close the GitHub pane")
	}
}

// TestSettingsListsGithub: the settings top page includes the GitHub entry.
func TestSettingsListsGithub(t *testing.T) {
	m := tuiModel{settings: &settingsModel{page: settingsTop}}
	if out := m.renderSettings(); !strings.Contains(out, "GitHub") {
		t.Errorf("settings should list a GitHub entry\n%s", out)
	}
}
