package main

import (
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"testing"
)

// canOpenNewWindow reports whether openInNewWindow has a backend it can use
// in the current environment. The Enter key uses this to choose between
// "open in a new window" (default) and "attach in-place, replacing the TUI"
// (the graceful fallback).
// canOpenNewWindow reports whether we can spawn a sibling window/tab for an
// attach. It's a var so tests can force the in-process (quit-to-attach) path
// deterministically regardless of GOOS (darwin/windows would otherwise always
// be true, and macOS would try to script Terminal).
var canOpenNewWindow = func() bool {
	// A TEST BINARY NEVER OPENS A WINDOW ON SOMEBODY'S SCREEN.
	//
	// This is not defensive; it is a bug caught in the act. TMUX is always set
	// inside an agent's pane, so this returned true under `go test` and any test
	// reaching an opener ran a REAL `tmux new-window` against the operator's live
	// session. Found as three stray windows in another agent's session:
	//
	//     agent-d3:2  github-connect  (dejima.test)
	//     agent-d3:3  github-connect  (dejima.test)
	//     agent-d3:4  github-connect  (dejima.test)
	//
	// dejima.test is the test binary. The operator had been reporting "a script
	// took over my terminal" for a week; this is one of the scripts.
	//
	// Individual tests do stub this to false, and that is exactly the problem —
	// it relies on every test remembering, and the ones that reached an opener
	// through a key handler did not know they were about to. Tests that WANT the
	// window path can still stub this true; the var is unchanged.
	if testing.Testing() {
		return false
	}
	return os.Getenv("TMUX") != "" || goruntime.GOOS == "darwin" || goruntime.GOOS == "windows"
}

// openInNewWindow launches `dejima connect <name>` in a separate window so the
// TUI can stay up as an overview. tmux is the portable path (a sibling window);
// macOS can script Terminal/iTerm directly. Elsewhere we point the user at the
// manual fallback. agentLabel, when non-empty, is the agent's user-set label used
// for the tab title (the freshly-added-agent case, where the model list is still
// stale); pass "" to resolve the title from current state.
//
// Critically, the child inherits the TUI's *current* connection target via
// DEJIMA_HOST — which may differ from the env that launched the TUI if the user
// switched profiles — so the new window hits the same daemon.
func (m tuiModel) openInNewWindow(name, agentID, agentLabel string) error {
	return m.openAgentWindow("connect", name, agentID, agentLabel, nil)
}

// openAgentLogsWindow opens `dejima logs <name> [--agent id] --follow` in a
// separate window — the open path for headless agents, which have no attach
// surface.
func (m tuiModel) openAgentLogsWindow(name, agentID, agentLabel string) error {
	return m.openAgentWindow("logs", name, agentID, agentLabel, []string{"--follow"})
}

// windowLabel builds a new tab's title from the user's *manually-set* names: the
// island's Title (falling back to its slug Name) and the agent's Label (falling
// back to its id). A primary/only agent renders as the island alone. agentLabel
// overrides the model lookup for the agent part — needed right after adding an
// agent, when m.islands hasn't refreshed to include it yet.
func (m tuiModel) windowLabel(name, agentID, agentLabel string) string {
	island := name
	if isl, ok := m.islandByName(name); ok {
		island = islandDisplay(isl)
		if agentLabel == "" && agentID != "" {
			for _, a := range isl.Agents {
				if a.ID == agentID {
					agentLabel = a.Label
					break
				}
			}
		}
	}
	suffix := agentLabel
	if suffix == "" {
		suffix = agentID
	}
	if suffix == "" {
		return island
	}
	return island + "/" + suffix
}

// openAgentWindow launches `dejima <verb> <name> [--agent id] [extra…]` in a
// separate window so the TUI can stay up as an overview. tmux is the portable
// path (a sibling window); macOS scripts Terminal/iTerm; Windows opens a tab.
//
// Critically, the child inherits the TUI's *current* connection target via
// DEJIMA_HOST — which may differ from the env that launched the TUI if the user
// switched profiles — so the new window hits the same daemon. extra args are
// trusted constants (e.g. "--follow"), so they're appended without quoting.
func (m tuiModel) openAgentWindow(verb, name, agentID, agentLabel string, extra []string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	// Session tabs are titled by the user's manually-set names ("<island Title>-
	// <agent Label>", just the island when it's the primary/only agent), falling
	// back to the durable handles when none is set. The dashboard's own tab is
	// titled "dejima" (set via tea.SetWindowTitle at startup).
	winLabel := m.windowLabel(name, agentID, agentLabel)
	// A shell command string: pin DEJIMA_HOST + the resolved tab title (so the
	// spawned session's OSC title uses the agent's LABEL, not its id), then exec
	// the verb. winLabel already prefers the label and falls back to the id.
	inner := fmt.Sprintf("DEJIMA_HOST=%s DEJIMA_TAB_TITLE=%s exec %s %s %s",
		shquote(m.activeHost), shquote(winLabel), shquote(exe), verb, shquote(name))
	if agentID != "" {
		inner += " --agent " + shquote(agentID)
	}
	for _, e := range extra {
		inner += " " + e
	}

	switch {
	case os.Getenv("TMUX") != "":
		return exec.Command("tmux", "new-window", "-n", winLabel, inner).Run()
	case goruntime.GOOS == "darwin":
		return openMacTerminal(inner)
	case goruntime.GOOS == "windows":
		return openWindowsTerminal(exe, verb, name, agentID, winLabel, extra, m.activeHost)
	default:
		return fmt.Errorf("open-in-new-window needs tmux, macOS, or Windows — run the TUI inside tmux, or `dejima %s %s` in another terminal", verb, name)
	}
}

// tmuxWindowIndex finds the window with exactly this name in `tmux list-windows`
// output, formatted as "<index>\t<name>" per line.
//
// Exact match, deliberately: tmux's own `-t <name>` targeting falls back to
// prefix and fnmatch searching, so selecting "host/api" could land on
// "host/api-staging" — picking the wrong window is worse than opening a new one.
func tmuxWindowIndex(listOutput, name string) (string, bool) {
	for _, line := range strings.Split(listOutput, "\n") {
		idx, wname, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok && wname == name {
			return idx, true
		}
	}
	return "", false
}

// tmuxFocusWindow switches to an existing window by name, reporting whether it
// found one.
//
// Each opener below ran `tmux new-window` unconditionally, so a second press
// produced a second window instead of returning to the first. For a flow that is
// a singleton by nature — one GitHub sign-in, one WSL setup, one attachment to a
// given host terminal — that is a leak the operator has to clean up by hand.
// Reported from a real session as tmux "adding layers of github-connect": every
// layer an abandoned device-flow poll, none of them distinguishable from the one
// that is actually live.
func tmuxFocusWindow(name string) bool {
	if os.Getenv("TMUX") == "" {
		return false
	}
	out, err := exec.Command("tmux", "list-windows", "-F", "#{window_index}\t#{window_name}").Output()
	if err != nil {
		return false
	}
	idx, ok := tmuxWindowIndex(string(out), name)
	if !ok {
		return false
	}
	return exec.Command("tmux", "select-window", "-t", idx).Run() == nil
}

// openAgentGatewayWindow opens `dejima agent open <island> <agentID>` in a new
// window — the forward-and-open-browser flow for a gateway agent (OpenClaw,
// Letta, Goose). Its own window because it holds an SSH tunnel until closed.
// NOT exec: on failure (SSH façade off, etc.) the window stays so the operator
// reads the error instead of a flash.
func (m tuiModel) openAgentGatewayWindow(island, agentID string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	title := "ui-" + island
	// agent id is a POSITIONAL arg to `agent open`, not --agent.
	arg := shquote(island)
	if agentID != "" {
		arg += " " + shquote(agentID)
	}
	inner := fmt.Sprintf("DEJIMA_HOST=%s DEJIMA_TAB_TITLE=%s %s agent open %s; printf '\n[gateway tunnel closed — press Enter]'; read _",
		shquote(m.activeHost), shquote(title), shquote(exe), arg)
	switch {
	case os.Getenv("TMUX") != "":
		// Return to the existing one rather than stacking another.
		if tmuxFocusWindow(title) {
			return nil
		}
		return exec.Command("tmux", "new-window", "-n", title, inner).Run()
	case goruntime.GOOS == "darwin":
		return openMacTerminal(inner)
	case goruntime.GOOS == "windows":
		return openWindowsTerminal(exe, "agent open", island, agentID, title, nil, m.activeHost)
	default:
		return fmt.Errorf("open-in-new-window needs tmux, macOS, or Windows — run `dejima agent open %s` in another terminal", island)
	}
}

// openHostTermWindow attaches to a host terminal (`dejima term attach <id>`) in a
// separate window/tab, so the dashboard stays up — the same "don't hijack the
// current view" behavior island shells and agent sessions already get. The tab is
// titled "host/<label>" (falling back to the id), and the child inherits the TUI's
// current DEJIMA_HOST so it hits the same daemon. The caller falls back to the
// in-place quit-to-attach path when canOpenNewWindow() is false.
func (m tuiModel) openHostTermWindow(id, label string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	title := "host/" + id
	if label != "" {
		title = "host/" + label
	}
	inner := fmt.Sprintf("DEJIMA_HOST=%s DEJIMA_TAB_TITLE=%s exec %s term attach %s",
		shquote(m.activeHost), shquote(title), shquote(exe), shquote(id))
	switch {
	case os.Getenv("TMUX") != "":
		// Return to the existing one rather than stacking another.
		if tmuxFocusWindow(title) {
			return nil
		}
		return exec.Command("tmux", "new-window", "-n", title, inner).Run()
	case goruntime.GOOS == "darwin":
		return openMacTerminal(inner)
	case goruntime.GOOS == "windows":
		// verb "term attach" is two trusted words; id is the terminal handle.
		return openWindowsTerminal(exe, "term attach", id, "", title, nil, m.activeHost)
	default:
		return fmt.Errorf("open-in-new-window needs tmux, macOS, or Windows — run the TUI inside tmux, or `dejima term attach %s` in another terminal", id)
	}
}

// openWSLSetupWindow launches `dejima wsl setup` in a separate window/tab — the
// guided "make this Windows box a real Dejima host" flow, offered from the
// connection switcher when picking `local` fails on Windows.
//
// Its own window for the same reasons `github connect` gets one: it is long
// (an Ubuntu download plus an image build), it PROMPTS before creating the
// distro and before installing Docker, and it must not be driven from inside a
// Bubble Tea render loop that owns the terminal. Deliberately NOT `exec`-ed and
// not run in-place: the TUI keeps running, so a user who changes their mind
// still has their dashboard and their existing connection untouched.
//
// No DEJIMA_HOST is pinned. Every other spawned window inherits the TUI's active
// target so it talks to the same daemon; this one is *creating* a target, and
// setup writes and activates its own profile when it succeeds.
func (m tuiModel) openWSLSetupWindow() error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	title := "wsl-setup"
	inner := fmt.Sprintf("DEJIMA_TAB_TITLE=%s %s wsl setup", shquote(title), shquote(exe))
	switch {
	case os.Getenv("TMUX") != "":
		// Return to the existing one rather than stacking another.
		if tmuxFocusWindow(title) {
			return nil
		}
		return exec.Command("tmux", "new-window", "-n", title, inner).Run()
	case goruntime.GOOS == "windows":
		// verb "wsl setup" is two trusted words, like "term attach".
		return openWindowsTerminal(exe, "wsl setup", "", "", title, nil, "")
	default:
		// macOS/Linux can host dejimad directly, so there is no WSL flow to open;
		// the switcher only offers this on Windows. Named rather than silent in
		// case a future caller forgets that gate.
		return fmt.Errorf("`dejima wsl setup` applies to Windows only — on %s run `dejima onboard`", goruntime.GOOS)
	}
}

// windowsRunCommand builds the `dejima <verb> …` command line for a spawned
// Windows tab. The agent id's placement is verb-specific: `agent open` takes it
// POSITIONALLY (`agent open <island> <id>`), while connect/logs take `--agent
// <id>`. Getting this wrong is why opening an OpenClaw console on Windows failed
// with "unknown flag: --agent".
func windowsRunCommand(exe, verb, name, agentID string, extra []string) string {
	run := `"` + exe + `" ` + verb + ` ` + name
	if agentID != "" {
		if verb == "agent open" {
			run += " " + agentID // positional, not a flag
		} else {
			run += " --agent " + agentID
		}
	}
	for _, e := range extra {
		run += " " + e
	}
	return run
}

// windowsInnerCommand builds the cmd.exe command line the spawned window runs.
//
// Split out so the SHAPE of it is testable: there is no cmd.exe on the machines
// that run these tests, and the property that matters — that the window survives
// the command exiting — is a property of this string.
func windowsInnerCommand(exe, verb, name, agentID string, extra []string, host, title string) string {
	run := windowsRunCommand(exe, verb, name, agentID, extra)
	// Pass the resolved tab title (label, not id) so the spawned dejima's OSC
	// title matches the tab name. Strip cmd.exe-special chars from the title
	// before interpolating it (it's a user label, unlike name/agentID/host,
	// which are refused outright above).
	safeTitle := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`"&|<>^%!`, r) {
			return -1
		}
		return r
	}, title)
	// KEEP THE WINDOW OPEN AFTER THE COMMAND EXITS.
	//
	// `cmd /c` closes the window the instant its command returns, so anything
	// that finishes — successfully or not — vanishes before it can be read. The
	// operator saw this twice: `github connect` "pulled up a terminal briefly
	// then snapped back", and the gateway UI window did the same. Both were
	// reported by the TUI as opened, which they were; what they were not was
	// READABLE.
	//
	// The unix branches already do this (`printf …; read _`). `&` rather than
	// `&&` so it runs on failure too — failure is precisely the case worth
	// reading. Long-lived commands (attaching to an agent or a host terminal)
	// reach the pause only when the operator detaches, matching unix.
	return fmt.Sprintf(
		`set "DEJIMA_HOST=%s"&& set "DEJIMA_TAB_TITLE=%s"&& %s& echo.& echo [this window stays open so you can read the output - press any key to close]& pause >nul`,
		host, safeTitle, run)
}

// openWindowsTerminal opens `dejima <verb>` in a new Windows Terminal tab
// (when wt.exe is around) or a new classic console window. DEJIMA_HOST is
// pinned via a cmd wrapper because wt/start don't reliably inherit the
// caller's environment.
func openWindowsTerminal(exe, verb, name, agentID, title string, extra []string, host string) error {
	// Island names, agent ids, and hosts feed a cmd.exe command line, which has
	// no sane quoting — refuse anything beyond the characters they legitimately
	// use. (title is passed as a discrete exec arg, not interpolated, so it's not
	// part of this check — it can carry spaces from a Title/Label.)
	for _, s := range []string{name, agentID, host} {
		if strings.ContainsAny(s, `"&|<>^%!`) {
			return fmt.Errorf("can't open a window for %q — run `dejima %s %s` manually", s, verb, name)
		}
	}
	// KEEP THE WINDOW OPEN AFTER THE COMMAND EXITS.
	//
	// `cmd /c` closes the window the instant its command returns, so anything
	// that finishes — successfully or not — vanishes before it can be read. The
	// operator saw this twice: `github connect` "pulled up a terminal briefly
	// then snapped back", and the gateway UI window did the same. Both were
	// reported by the TUI as opened, which they were; what they were not was
	// READABLE.
	//
	// The unix branches already do this (`printf …; read _`). This is the
	// Windows equivalent, and `&` rather than `&&` so it runs on failure too —
	// failure is precisely the case worth reading.
	//
	// Long-lived commands (attaching to an agent or a host terminal) reach the
	// pause only when the operator detaches, which is the same behaviour their
	// unix counterparts already have.
	inner := windowsInnerCommand(exe, verb, name, agentID, extra, host, title)
	if wt, err := exec.LookPath("wt.exe"); err == nil {
		// -w 0 targets the CURRENT Windows Terminal window (a new tab in it).
		// -w -1 / "new" would force a separate window every time — which is the
		// opposite of what we want here.
		return exec.Command(wt, "-w", "0", "new-tab", "--title", title, "cmd", "/c", inner).Run()
	}
	return exec.Command("cmd", "/c", "start", title, "cmd", "/c", inner).Run()
}

// editorWorkspace is where every island mounts its working tree — the folder we
// open editors into, instead of the SSH login home (which holds only dotfiles).
const editorWorkspace = "/workspace"

// editorCandidates is the auto-detect order when the user hasn't picked an
// editor: VS Code first, then its popular forks. All of them are VS Code
// derivatives that accept `--remote ssh-remote+<host> <path>`.
var editorCandidates = []string{"code", "cursor", "windsurf", "antigravity", "code-insiders"}

// openInEditor launches the user's Remote-SSH editor connected to the island and
// opened straight at /workspace — so they never browse to the repo. `preferred`
// is the configured CLI command (clientcfg.Editor); empty means auto-detect the
// first candidate on PATH. The alias matches the `dejima-<island>` Host that
// `ssh config`/`enroll` writes; the remote authority for an ssh-config host is
// `ssh-remote+<alias>`. Forks (Cursor/Windsurf/Antigravity) share VS Code's flag.
func openInEditor(alias, preferred string) error {
	remote := "ssh-remote+" + alias
	if preferred != "" {
		exe, err := exec.LookPath(preferred)
		if err != nil {
			return fmt.Errorf("editor %q not found on PATH — change it in settings (,) or install its CLI", preferred)
		}
		return exec.Command(exe, "--remote", remote, editorWorkspace).Run()
	}
	for _, bin := range editorCandidates {
		if exe, err := exec.LookPath(bin); err == nil {
			return exec.Command(exe, "--remote", remote, editorWorkspace).Run()
		}
	}
	return fmt.Errorf("no editor CLI found on PATH — set one in settings (,) or install code/cursor; or connect manually: Remote-SSH → %s, open %s", alias, editorWorkspace)
}

// openMacTerminal opens the command in a new TAB of the current window when the
// terminal supports it (matching the new-tab behavior of tmux and Windows
// Terminal), falling back to the AppleScript path otherwise. iTerm2 gets a tab
// via osascript; WezTerm and kitty each get one via their own CLI (guarded so a
// missing/disabled CLI degrades rather than errors); everything else — including
// Terminal.app, whose AppleScript has no first-class tab support, and Ghostty,
// which has no reliable "open tab with command" CLI — falls through to the
// existing osascript path (a new iTerm tab or Terminal.app window).
func openMacTerminal(inner string) error {
	kind := resolveTerminalKind() // DEJIMA_TERMINAL / clientcfg override, else auto-detect
	switch kind {
	case terminalWezTerm:
		if err := spawnWezTermTab(inner); err == nil {
			return nil
		}
		// wezterm CLI missing or spawn failed → fall through to osascript.
	case terminalKitty:
		if err := spawnKittyTab(inner); err == nil {
			return nil
		}
		// kitty CLI missing or remote control off → fall through to osascript.
	case terminalGhostty:
		if err := spawnGhosttyWindow(inner); err == nil {
			return nil
		}
		// Ghostty launch failed (not installed under that name?) → osascript fallback.
	}
	// iTerm/Terminal/Warp/unknown: the osascript path (iTerm tab or a Terminal.app
	// window). Warp has no scriptable new-window-with-command CLI, so it lands here
	// too — the setting still lets a Warp user force a scriptable terminal.
	return openMacTerminalOSA(kind, inner)
}

// openMacTerminalOSA is the AppleScript path: a new iTerm tab in the current
// window, or — on Terminal.app, whose AppleScript has no first-class tab
// support — a new window. This is the durable fallback for every terminal whose
// native new-tab CLI is unavailable.
func openMacTerminalOSA(kind terminalKind, inner string) error {
	var script string
	if kind == terminalITerm2 {
		script = fmt.Sprintf(`tell application "iTerm"
  tell current window to set t to (create tab with default profile)
  tell current session of t to write text %s
  activate
end tell`, appleStr(inner))
	} else {
		script = fmt.Sprintf(`tell application "Terminal"
  do script %s
  activate
end tell`, appleStr(inner))
	}
	return exec.Command("osascript", "-e", script).Run()
}

// shquote single-quotes a string for safe inclusion in a /bin/sh command.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleStr renders a Go string as an AppleScript double-quoted literal.
func appleStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// openAuthPushWindow runs `dejima auth push` in its own window.
//
// Its own window because it may prompt, and because on a machine where the
// Claude login lives in a keychain the OS asks for permission — which cannot
// happen inside the dashboard's alternate screen.
func (m tuiModel) openAuthPushWindow() error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	title := "auth-push"
	inner := fmt.Sprintf("DEJIMA_HOST=%s DEJIMA_TAB_TITLE=%s exec %s auth push",
		shquote(m.activeHost), shquote(title), shquote(exe))
	switch {
	case os.Getenv("TMUX") != "":
		if tmuxFocusWindow(title) {
			return nil
		}
		return exec.Command("tmux", "new-window", "-n", title, inner).Run()
	case goruntime.GOOS == "darwin":
		return openMacTerminal(inner)
	case goruntime.GOOS == "windows":
		return openWindowsTerminal(exe, "auth push", "", "", title, nil, m.activeHost)
	default:
		return fmt.Errorf("open-in-new-window needs tmux, macOS, or Windows — run `dejima auth push` in another terminal")
	}
}
