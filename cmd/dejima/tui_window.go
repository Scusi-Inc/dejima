package main

import (
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
)

// openInNewWindow launches `dejima connect <name>` in a separate window so the
// TUI can stay up as an overview. tmux is the portable path (a sibling window);
// macOS can script Terminal/iTerm directly. Elsewhere we point the user at the
// manual fallback.
//
// Critically, the child inherits the TUI's *current* connection target via
// DEJIMA_HOST — which may differ from the env that launched the TUI if the user
// switched profiles — so the new window hits the same daemon.
func (m tuiModel) openInNewWindow(name string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "dejima"
	}
	// A shell command string: pin DEJIMA_HOST, then exec the connect.
	inner := fmt.Sprintf("DEJIMA_HOST=%s exec %s connect %s",
		shquote(m.activeHost), shquote(exe), shquote(name))

	switch {
	case os.Getenv("TMUX") != "":
		return exec.Command("tmux", "new-window", "-n", name, inner).Run()
	case goruntime.GOOS == "darwin":
		return openMacTerminal(inner)
	default:
		return fmt.Errorf("open-in-new-window needs tmux or macOS — run the TUI inside tmux, or `dejima connect %s` in another terminal", name)
	}
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
