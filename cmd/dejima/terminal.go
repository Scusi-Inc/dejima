package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aoos/dejima/internal/paths"
)

// terminalKind classifies the terminal emulator the TUI is running inside. It
// drives two things: openMacTerminal picks a native new-tab path per emulator,
// and the first-run nudge steers Apple Terminal users (no tabs, no OSC 52
// clipboard) toward a tab-capable terminal.
type terminalKind int

const (
	terminalUnknown terminalKind = iota
	terminalITerm2
	terminalAppleTerminal
	terminalWezTerm
	terminalKitty
	terminalGhostty
)

// classifyTerminal identifies the terminal from its environment signals. It's a
// pure function — the env values are passed in rather than read here — so it's
// unit-testable without touching the real environment, mirroring how
// parseTailscaleIPv4 is split from its exec in tailscale.go.
//
// termProgram is $TERM_PROGRAM, term is $TERM, kittyWindowID is $KITTY_WINDOW_ID.
// kitty is the odd one out: it commonly leaves TERM_PROGRAM unset and instead
// advertises itself through TERM=xterm-kitty and/or KITTY_WINDOW_ID.
func classifyTerminal(termProgram, term, kittyWindowID string) terminalKind {
	switch termProgram {
	case "iTerm.app":
		return terminalITerm2
	case "Apple_Terminal":
		return terminalAppleTerminal
	case "WezTerm":
		return terminalWezTerm
	case "ghostty":
		return terminalGhostty
	}
	if term == "xterm-kitty" || kittyWindowID != "" {
		return terminalKitty
	}
	return terminalUnknown
}

// currentTerminal classifies the terminal from the live environment.
func currentTerminal() terminalKind {
	return classifyTerminal(
		os.Getenv("TERM_PROGRAM"),
		os.Getenv("TERM"),
		os.Getenv("KITTY_WINDOW_ID"),
	)
}

// spawnWezTermTab opens inner in a NEW TAB of the current WezTerm window via
// `wezterm cli spawn`. Returns an error — so the caller can fall back to the
// osascript path — when the wezterm CLI is missing or the spawn fails. inner is
// passed as a discrete argv element to `/bin/sh -c`, so it needs no extra
// quoting (unlike the AppleScript path).
func spawnWezTermTab(inner string) error {
	exe, err := exec.LookPath("wezterm")
	if err != nil {
		return err
	}
	return exec.Command(exe, "cli", "spawn", "--", "/bin/sh", "-c", inner).Run()
}

// spawnKittyTab opens inner in a NEW TAB of the current kitty window via kitty's
// remote-control protocol (`kitty @ launch --type=tab`). Remote control has to
// be enabled (allow_remote_control + a listen socket); when it isn't — or the
// kitty CLI is missing — this returns an error so the caller falls back to the
// osascript path rather than hard-failing.
func spawnKittyTab(inner string) error {
	exe, err := exec.LookPath("kitty")
	if err != nil {
		return err
	}
	if !kittyRemoteControlReady(exe) {
		return fmt.Errorf("kitty remote control not enabled")
	}
	return exec.Command(exe, "@", "launch", "--type=tab", "--cwd", "current", "/bin/sh", "-c", inner).Run()
}

// kittyRemoteControlReady reports whether kitty's remote control is reachable —
// either a listen socket is exported ($KITTY_LISTEN_ON) or `kitty @ ls`
// succeeds. Without it, `kitty @ launch` would error, so we probe first and let
// the caller fall back cleanly.
func kittyRemoteControlReady(exe string) bool {
	if os.Getenv("KITTY_LISTEN_ON") != "" {
		return true
	}
	return exec.Command(exe, "@", "ls").Run() == nil
}

// macTermNudge is the one-time hint for Apple Terminal users. Terminal.app opens
// every agent in a separate WINDOW (no tabs) and can't copy an agent's auth URL
// via OSC 52, so we gently recommend a tab-capable terminal. It fires exactly
// once: the first time it returns non-empty it persists a marker so later
// launches stay silent. Empty on every other terminal and once dismissed.
func macTermNudge() string {
	if currentTerminal() != terminalAppleTerminal {
		return ""
	}
	if macTermNudgeDismissed() {
		return ""
	}
	_ = writeMacTermNudgeMarker() // best-effort; a failed write just re-nudges next time
	return "Terminal.app opens agents in separate windows & can't copy URLs — iTerm2/WezTerm/Ghostty give tabs + clipboard"
}

func macTermNudgeMarkerPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "mac-terminal-nudge-dismissed"), nil
}

func macTermNudgeDismissed() bool {
	p, err := macTermNudgeMarkerPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func writeMacTermNudgeMarker() error {
	p, err := macTermNudgeMarkerPath()
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte("dismissed\n"), 0o600)
}
