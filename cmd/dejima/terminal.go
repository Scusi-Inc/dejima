package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aoos/dejima/internal/clientcfg"
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
	terminalWarp
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
	case "WarpTerminal":
		return terminalWarp
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

// parseTerminalKind maps a user-facing terminal name (DEJIMA_TERMINAL or the
// clientcfg setting) to a kind. ok is false for "" / "auto" (no override → the
// caller auto-detects) and for anything unrecognized.
func parseTerminalKind(s string) (kind terminalKind, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto", "auto-detect":
		return terminalUnknown, false
	case "terminal", "apple", "apple_terminal", "terminal.app":
		return terminalAppleTerminal, true
	case "iterm", "iterm2", "iterm.app":
		return terminalITerm2, true
	case "ghostty":
		return terminalGhostty, true
	case "wezterm":
		return terminalWezTerm, true
	case "kitty":
		return terminalKitty, true
	case "warp":
		return terminalWarp, true
	default:
		return terminalUnknown, false
	}
}

// resolveTerminalKind is the terminal openMacTerminal launches into, honoring the
// precedence: DEJIMA_TERMINAL env > clientcfg.Terminal setting > auto-detect from
// $TERM_PROGRAM. The override fixes terminals we detect but couldn't previously
// launch (Ghostty fell through to Apple Terminal), and lets a user force one.
func resolveTerminalKind() terminalKind {
	if k, ok := parseTerminalKind(os.Getenv("DEJIMA_TERMINAL")); ok {
		return k
	}
	if cfg, err := clientcfg.Load(); err == nil {
		if k, ok := parseTerminalKind(cfg.Terminal); ok {
			return k
		}
	}
	return currentTerminal()
}

// spawnGhosttyWindow opens inner in a NEW Ghostty window via `open -na Ghostty
// --args -e /bin/sh -c <inner>`. inner is a shell command string; it's passed as
// a single discrete argv element (not through a shell here), so `/bin/sh -c`
// receives it intact — no extra quoting needed. Returns an error (→ osascript
// fallback) if the launch fails.
func spawnGhosttyWindow(inner string) error {
	return exec.Command("open", "-na", "Ghostty", "--args", "-e", "/bin/sh", "-c", inner).Run()
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
