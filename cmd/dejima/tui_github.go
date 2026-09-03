package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// githubView is the self-serve "GitHub" settings pane: it lists the caller's
// GitHub identities, badges islands whose named identity no longer resolves
// (clone/push will fail until reconnected), and runs the guided device-flow
// sign-in IN THE PANE (tui_github_device.go). It used to spawn `dejima github
// connect` in a new window — which handed the operator to a surface this pane
// could not observe, and on Windows died instantly while the pane reported that
// it had opened.
type githubView struct {
	loading    bool
	identities []api.GitHubIdentityView
	dangling   []api.DanglingIdentityPin
	cursor     int
	err        string
	notice     string
	// connect is a device-flow sign-in running INSIDE this pane. Non-nil while
	// one is in progress; it owns the keys and the body while it is. See
	// tui_github_device.go.
	connect *deviceFlow
}

type githubIdentitiesMsg struct {
	identities []api.GitHubIdentityView
	dangling   []api.DanglingIdentityPin
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
		resp, err := c.ListGitHubIdentitiesFull(ctx)
		return githubIdentitiesMsg{identities: resp.Identities, dangling: resp.Dangling, err: err}
	}
}

// githubKey drives the GitHub pane. [c]/⏎ starts the guided sign-in here; [r]
// reloads; [esc]/[q] closes.
func (m tuiModel) githubKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.github
	// A sign-in in progress owns the keys: list navigation is meaningless here,
	// and [c] restarting a flow the operator is halfway through would invalidate
	// a code they may already have typed into a browser.
	if v.connect != nil {
		// ...except a failed flow, where [c] is the retry the pane offers.
		if v.connect.state != deviceFlowFailed || (msg.String() != "c" && msg.String() != "enter") {
			mm, cmd, _ := m.deviceFlowKey(msg)
			return mm, cmd
		}
	}
	switch msg.String() {
	case "esc", "ctrl+[", "q":
		m.github = nil
		return m, nil
	case "r":
		v.loading = true
		v.notice = ""
		return m, m.loadGithubIdentitiesCmd()
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
		}
		return m, nil
	case "down", "j":
		if v.cursor < len(v.identities)-1 {
			v.cursor++
		}
		return m, nil
	case "c", "enter":
		// REFRESH the highlighted identity, by name, and make it the default.
		//
		// This used to run a bare `dejima github connect`, which stores under the
		// fixed name "github" and does not take the default. An operator whose
		// `aoos` token had expired pressed [c] here, got a second identity for the
		// same login that no island used, refreshed it, and watched eight islands
		// keep failing. The pane created the confusion it was being used to clear.
		name := ""
		if id, ok := v.selected(); ok {
			name = id.Name
		}
		v.notice, v.err = "", ""
		v.connect = &deviceFlow{name: name, makeDefault: true, state: deviceFlowStarting}
		return m, m.startDeviceFlowCmd()
	case "n":
		// A genuinely NEW identity (a second GitHub account), as opposed to
		// refreshing one you have. Separated because they were the same key, and
		// the one people actually wanted was the refresh. An empty name lets the
		// daemon name it rather than overwriting the highlighted one.
		v.notice, v.err = "", ""
		v.connect = &deviceFlow{state: deviceFlowStarting}
		return m, m.startDeviceFlowCmd()
	}
	return m, nil
}

// selected returns the highlighted identity, if any.
func (v *githubView) selected() (api.GitHubIdentityView, bool) {
	if v.cursor < 0 || v.cursor >= len(v.identities) {
		return api.GitHubIdentityView{}, false
	}
	return v.identities[v.cursor], true
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

	if v.connect != nil {
		return b.String() + v.renderDeviceFlow(m.now())
	}
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
		def := ""
		for _, id := range v.identities {
			if id.Default {
				def = id.Name
			}
		}
		unusedDefault, mostUsed := splitByUsage(v.identities, def)
		for i, id := range v.identities {
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
			// The mark has to MEAN something. It used to be a ✓ on every row,
			// including one whose token had been dead for a month — the pane
			// asserting health it had never checked, next to the exact identity
			// the operator needed to distrust.
			mark := styleRunning.Render("✓")
			if len(id.Islands) == 0 {
				mark = styleMuted.Render("·")
			}
			if id.Name == unusedDefault {
				mark = styleWaiting.Render("⚠")
			}
			row := fmt.Sprintf("%s %s%s", mark,
				styleAccent.Render(id.Name)+styleMuted.Render(login), styleMuted.Render(tags))
			if i == v.cursor {
				row = styleSelected.Render("▶ ") + row
			} else {
				row = "  " + row
			}
			b.WriteString("  " + row + "\n")
			// The two facts the old pane omitted, and the only ones that separate
			// two identities for the same GitHub login.
			b.WriteString(styleMuted.Render(fmt.Sprintf("        %s · token %s\n",
				islandsCell(id.Islands), refreshedAge(id.UpdatedAt))))
		}
		b.WriteString("\n")
		if unusedDefault != "" {
			b.WriteString(styleWaiting.Render(fmt.Sprintf(
				"  ⚠ the default (%s) is used by NO island — refreshing it changes nothing.", unusedDefault)))
			b.WriteString("\n")
			b.WriteString(styleMuted.Render(fmt.Sprintf(
				"    Islands use %q. Select it above and press [c].", mostUsed)))
			b.WriteString("\n\n")
		}
		for _, d := range v.dangling {
			b.WriteString(styleErrored.Render(fmt.Sprintf(
				"  ⚠ %s names identity %q, which no longer exists — it has NO credential.", d.Island, d.Identity)))
			b.WriteString("\n")
			b.WriteString(styleMuted.Render("    Fix:  dejima github repoint " + d.Island + " <name>"))
			b.WriteString("\n\n")
		}
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

	b.WriteString(styleMuted.Render("  [↑↓] select   [c] refresh this identity   [n] new identity   [r] reload   [esc] back"))
	return b.String()
}
