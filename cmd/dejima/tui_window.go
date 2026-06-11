package main

import (
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
)

// canOpenNewWindow reports whether openInNewWindow has a backend it can use
// in the current environment. The Enter key uses this to choose between
// "open in a new window" (default) and "attach in-place, replacing the TUI"
// (the graceful fallback).
func canOpenNewWindow() bool {
	return os.Getenv("TMUX") != "" || goruntime.GOOS == "darwin" || goruntime.GOOS == "windows"
}

// openInNewWindow launches `dejima connect <name>` in a separate window so the
// TUI can stay up as an overview. tmux is the portable path (a sibling window);
// macOS can script Terminal/iTerm directly. Elsewhere we point the user at the
// manual fallback.
//
// Critically, the child inherits the TUI's *current* connection target via
// DEJIMA_HOST — which may differ from the env that launched the TUI if the user
// switched profiles — so the new window hits the same daemon.
func (m tuiModel) openInNewWindow(name, agentID string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	// A shell command string: pin DEJIMA_HOST, then exec the connect. A specific
	// agent is targeted via `--agent`; the bare name attaches to the primary.
	inner := fmt.Sprintf("DEJIMA_HOST=%s exec %s connect %s",
		shquote(m.activeHost), shquote(exe), shquote(name))
	winLabel := name
	if agentID != "" {
		inner += " --agent " + shquote(agentID)
		winLabel = name + "/" + agentID
	}

	switch {
	case os.Getenv("TMUX") != "":
		return exec.Command("tmux", "new-window", "-n", winLabel, inner).Run()
	case goruntime.GOOS == "darwin":
		return openMacTerminal(inner)
	case goruntime.GOOS == "windows":
		return openWindowsTerminal(exe, name, agentID, m.activeHost)
	default:
		return fmt.Errorf("open-in-new-window needs tmux, macOS, or Windows — run the TUI inside tmux, or `dejima connect %s` in another terminal", name)
	}
}

// openWindowsTerminal opens `dejima connect` in a new Windows Terminal tab
// (when wt.exe is around) or a new classic console window. DEJIMA_HOST is
// pinned via a cmd wrapper because wt/start don't reliably inherit the
// caller's environment.
func openWindowsTerminal(exe, name, agentID, host string) error {
	// Island names, agent ids, and hosts feed a cmd.exe command line, which has
	// no sane quoting — refuse anything beyond the characters they legitimately use.
	for _, s := range []string{name, agentID, host} {
		if strings.ContainsAny(s, `"&|<>^%!`) {
			return fmt.Errorf("can't open a window for %q — run `dejima connect %s` manually", s, name)
		}
	}
	connect := `"` + exe + `" connect ` + name
	if agentID != "" {
		connect += " --agent " + agentID
	}
	inner := fmt.Sprintf(`set "DEJIMA_HOST=%s"&& %s`, host, connect)
	if wt, err := exec.LookPath("wt.exe"); err == nil {
		return exec.Command(wt, "-w", "-1", "new-tab", "--title", name, "cmd", "/c", inner).Run()
	}
	return exec.Command("cmd", "/c", "start", name, "cmd", "/c", inner).Run()
}

// openMacTerminal opens the command in a new iTerm or Terminal.app window.
func openMacTerminal(inner string) error {
	var script string
	if os.Getenv("TERM_PROGRAM") == "iTerm.app" {
		script = fmt.Sprintf(`tell application "iTerm"
  set w to (create window with default profile)
  tell current session of w to write text %s
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
