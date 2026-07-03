package main

import (
	"encoding/base64"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// clipboardCopiedMsg is emitted after a copy attempt; notice is the transient
// confirmation shown in the TUI status line (via m.lastNotice).
type clipboardCopiedMsg struct{ notice string }

// osc52 builds the OSC-52 terminal escape that sets the system clipboard ("c")
// to s. Payload is base64 (standard alphabet, per the xterm spec), terminated
// with BEL. It carries no visible glyphs, so writing it mid-render can't corrupt
// the Bubble Tea screen.
func osc52(s string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\a"
}

// copyToClipboardCmd copies payload to the terminal clipboard via OSC-52 and
// returns a clipboardCopiedMsg carrying notice for the status line.
//
// OSC-52 is chosen deliberately over shelling out to pbcopy/xclip: `dejima`
// often runs inside the operator's tmux+SSH session, where a host-side pbcopy
// would target the wrong machine. An OSC-52 escape instead rides back through
// SSH + tmux (with clipboard passthrough enabled) to the operator's own terminal
// emulator — the case operators actually live in. The invite blob is short
// (~150 chars), well within OSC-52 limits.
func copyToClipboardCmd(payload, notice string) tea.Cmd {
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(osc52(payload))
		return clipboardCopiedMsg{notice: notice}
	}
}
