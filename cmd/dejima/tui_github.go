package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/githubid"
)

// githubView is the self-serve "GitHub" settings pane: it lists the caller's
// GitHub identities, badges islands whose named identity no longer resolves
// (clone/push will fail until reconnected), and drives the guided device-flow
// sign-in. The sign-in itself runs in its own window via the tested `dejima
// github connect` CLI (browser + polling belong outside the dashboard); the pane
// reloads on demand once the operator finishes there.
type githubView struct {
	loading    bool
	identities []githubid.Meta
	err        string
	notice     string
}

type githubIdentitiesMsg struct {
	identities []githubid.Meta
	err        error
}

func (m tuiModel) openGithubView() (tea.Model, tea.Cmd) {
	m.github = &githubView{loading: true}
	return m, m.loadGithubIdentitiesCmd()
}

func (m tuiModel) loadGithubIdentitiesCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ids, err := c.ListGitHubIdentities(ctx)
		return githubIdentitiesMsg{identities: ids, err: err}
	}
}

// githubKey drives the GitHub pane. [c]/⏎ spawns the guided sign-in in a new
// window; [r] reloads; [esc]/[q] closes.
func (m tuiModel) githubKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.github
	switch msg.String() {
	case "esc", "q":
		m.github = nil
		return m, nil
	case "r":
		v.loading = true
		v.notice = ""
		return m, m.loadGithubIdentitiesCmd()
	case "c", "enter":
		if !canOpenNewWindow() {
			v.notice = "run `dejima github connect` in a terminal to connect your GitHub, then [r] to reload"
			return m, nil
		}
		if err := m.openGithubConnectWindow(); err != nil {
			v.notice = "couldn't open a window — run `dejima github connect` in a terminal, then [r]"
		} else {
			v.notice = "opened `dejima github connect` — approve it on GitHub, then press [r] to reload"
		}
		return m, nil
	}
	return m, nil
}

// githubMissingCredIslands returns the names of islands whose named GitHub
// identity no longer resolves for their tenant — clone/push fails until the
// operator reconnects.
func (m tuiModel) githubMissingCredIslands() []string {
	var missing []string
	for _, isl := range m.islands {
		if isl.GitHubCredMissing {
			missing = append(missing, isl.Name)
		}
	}
	return missing
}

func (m tuiModel) renderGithubView() string {
	v := m.github
	var b strings.Builder
	b.WriteString(styleTitle.Render("GitHub"))
	b.WriteString("\n\n")

	if v.loading {
		b.WriteString(styleMuted.Render("  loading identities…"))
		return b.String()
	}
	if v.err != "" {
		b.WriteString(styleErrored.Render("  ⚠ " + truncate(v.err, 72)))
		b.WriteString("\n\n")
	}

	if len(v.identities) == 0 {
		b.WriteString(styleMuted.Render("  No GitHub identity yet — connect one to clone your private repos."))
		b.WriteString("\n\n")
	} else {
		b.WriteString(styleHeader.Render("  Your identities"))
		b.WriteString("\n")
		for _, id := range v.identities {
			tags := ""
			if id.Default {
				tags += " · default"
			}
			if id.Shared {
				tags += " · shared"
			}
			login := id.Login
			if login != "" {
				login = " (" + login + ")"
			}
			b.WriteString(fmt.Sprintf("    %s %s%s\n",
				styleRunning.Render("✓"),
				styleAccent.Render(id.Name)+styleMuted.Render(login),
				styleMuted.Render(tags)))
		}
		b.WriteString("\n")
	}

	if missing := m.githubMissingCredIslands(); len(missing) > 0 {
		b.WriteString(styleWaiting.Render("  ⚠ GitHub credential missing for: " + strings.Join(missing, ", ")))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("    Their clone/push will fail — reconnect below to fix."))
		b.WriteString("\n\n")
	}

	if v.notice != "" {
		b.WriteString(styleRunning.Render("  " + truncate(v.notice, 76)))
		b.WriteString("\n\n")
	}

	b.WriteString(styleMuted.Render("  [c] connect GitHub (guided sign-in)   [r] reload   [esc] back"))
	return b.String()
}
