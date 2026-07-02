package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// Attach-file keybind. Inside an attached island session this chord (default
// Ctrl-O, 0x0f) is intercepted CLIENT-side — before forwarding to the agent's
// PTY — to open a local path prompt (the attach minibuffer). Terminal drag-drop
// is unreliable over tmux+SSH, so typing/pasting a path is the robust path.
// Configurable via DEJIMA_ATTACH_KEY ("ctrl-o" | "ctrl-]" | "off"); the env is
// the escape hatch when Ctrl-O collides with a downstream binding.
const (
	attachChordCtrlO  = 0x0f // Ctrl-O
	attachChordCtrlRB = 0x1d // Ctrl-] (right bracket) — sits next to the Ctrl-\ summon chord
)

// configuredAttachKey returns the byte sequence that opens the attach minibuffer,
// per DEJIMA_ATTACH_KEY. Empty (nil) disables it. Only honored on island sessions
// on a real TTY (same gate as the paste bridge).
func configuredAttachKey() []byte {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEJIMA_ATTACH_KEY"))) {
	case "off", "none":
		return nil
	case "ctrl-]", "ctrl+]", "c-]":
		return []byte{attachChordCtrlRB}
	case "ctrl-o", "ctrl+o", "c-o", "":
		return []byte{attachChordCtrlO}
	default:
		return []byte{attachChordCtrlO}
	}
}

// attachKeyLabel is the human name of the configured attach chord (for banners
// and help), or "" when attach is disabled.
func attachKeyLabel() string {
	k := configuredAttachKey()
	if len(k) == 0 {
		return ""
	}
	if k[0] == attachChordCtrlRB {
		return "Ctrl-]"
	}
	return "Ctrl-O"
}

// splitOnAttach scans a stdin chunk for the attach chord. If present it returns
// the bytes before the chord (forwarded to the agent as usual) and the bytes
// after it (which seed the path buffer, so a paste on the same chunk isn't lost).
// Mirrors splitOnSummon.
func splitOnAttach(b, key []byte) (before, after []byte, hit bool) {
	if len(key) == 0 {
		return b, nil, false
	}
	i := bytes.Index(b, key)
	if i < 0 {
		return b, nil, false
	}
	return b[:i], b[i+len(key):], true
}

// stripBracketedPaste removes DEC-2004 paste markers so a path pasted into the
// minibuffer (which a terminal wraps in \e[200~…\e[201~) is collected as plain
// text. A split marker is rare here; the common single-chunk paste is handled.
func stripBracketedPaste(b []byte) []byte {
	s := string(b)
	s = strings.ReplaceAll(s, bpStart, "")
	s = strings.ReplaceAll(s, bpEnd, "")
	return []byte(s)
}

// attachState is a minimal raw-mode line editor for the in-session "attach a
// file" prompt. The terminal is in raw mode (no echo), so feed() echoes to w so
// the user sees what they type. It collects a path until Enter (submit) or
// Esc/Ctrl-C (cancel).
type attachState struct {
	buf []byte
	w   io.Writer
}

// feed consumes a stdin chunk. done → the user pressed Enter (path is the line);
// cancelled → Esc/Ctrl-C. Backspace edits; Ctrl-U clears; bracketed-paste markers
// are stripped; other control bytes are ignored. Bytes ≥ 0x20 (incl. UTF-8
// continuation bytes) are appended and echoed raw so multibyte paths survive.
func (s *attachState) feed(b []byte) (done, cancelled bool, path string) {
	b = stripBracketedPaste(b)
	for _, c := range b {
		switch {
		case c == 0x1b || c == 0x03: // Esc / Ctrl-C → cancel
			return false, true, ""
		case c == '\r' || c == '\n': // Enter → submit
			return true, false, string(s.buf)
		case c == 0x7f || c == 0x08: // Backspace / Ctrl-H
			if len(s.buf) > 0 {
				s.buf = s.buf[:len(s.buf)-1]
				_, _ = io.WriteString(s.w, "\b \b")
			}
		case c == 0x15: // Ctrl-U → clear the line
			for range s.buf {
				_, _ = io.WriteString(s.w, "\b \b")
			}
			s.buf = s.buf[:0]
		case c >= 0x20: // printable / UTF-8 byte
			s.buf = append(s.buf, c)
			_, _ = s.w.Write([]byte{c})
		default:
			// ignore other control bytes (Tab, etc.)
		}
	}
	return false, false, ""
}

// expandClientPath cleans a client-supplied path (quotes, file://, whitespace)
// and expands a leading ~ to the user's home dir. Existence is checked by the
// caller (os.Stat).
func expandClientPath(p string) string {
	p = cleanClientPath(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + strings.TrimPrefix(p, "~")
		}
	}
	return p
}

// stageLocalFile uploads a client-local file into the island's paste intake dir
// and returns the in-island path the agent can Read. Reused by drag-drop
// (islandPaste.drop), the attach minibuffer, and `dejima attach`.
func stageLocalFile(ctx context.Context, c *api.Client, island, localPath string) (islandPath string, err error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dest := fmt.Sprintf("%s/drop-%d-%s", pasteIntakeDir, time.Now().UnixNano(), filepath.Base(localPath))
	if err := c.WriteFile(ctx, island, dest, f); err != nil {
		return "", err
	}
	return dest, nil
}

// newAttachCmd is the non-interactive twin of the in-session attach chord: upload
// ANY client-local file into an island and inject its in-island path into the
// target agent's prompt as a bracketed paste (so a non-image file lands as a path
// the agent Reads; an image lands as an artifact — same pipeline as `dejima paste`).
func newAttachCmd() *cobra.Command {
	var agentID string
	var noInject bool
	cmd := &cobra.Command{
		Use:   "attach <island>[/<agent>] <path>",
		Short: "Attach a local file to an agent (upload it and point the agent at it).",
		Long: "Upload a client-local file into the island's intake dir and inject the staged " +
			"path into the target agent's prompt as a bracketed paste, so the agent can Read it. " +
			"Robust over tmux+SSH where terminal drag-drop isn't. The in-session twin is the " +
			attachKeyLabel() + " chord while attached (configurable via DEJIMA_ATTACH_KEY).\n\n" +
			"  dejima attach myrepo ./notes.md          # into the island's first interactive agent\n" +
			"  dejima attach myrepo/reviewer ~/key.p8   # into a specific agent",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, agent := splitIslandAgent(args[0])
			if agentID != "" {
				agent = agentID
			}
			localPath := expandClientPath(args[1])
			fi, err := os.Stat(localPath)
			if err != nil || !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a readable file", args[1])
			}

			c, err := client()
			if err != nil {
				return err
			}
			tmux, resolvedAgent, err := resolveAgentTmux(cmd, c, island, agent)
			if err != nil {
				return err
			}

			dest, err := stageLocalFile(cmd.Context(), c, island, localPath)
			if err != nil {
				return fmt.Errorf("stage file into island: %w", err)
			}
			fmt.Printf("📎 attached: %s → %s:%s\n", filepath.Base(localPath), island, dest)

			if noInject || tmux == "" {
				if tmux == "" && !noInject {
					fmt.Printf("agent %q has no interactive session; not injecting. Point it at: %s\n", resolvedAgent, dest)
				}
				return nil
			}
			res, err := c.ExecInIsland(cmd.Context(), island, []string{"tmux", "send-keys", "-t", tmux, "-l", bracketedPaste(dest)})
			if err != nil {
				return fmt.Errorf("inject path into agent %q: %w", resolvedAgent, err)
			}
			if res != nil && res.ExitCode != 0 {
				return fmt.Errorf("inject into agent %q: tmux send-keys exit %d", resolvedAgent, res.ExitCode)
			}
			fmt.Printf("injected → %s/%s\n", island, resolvedAgent)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "target agent id (default: the island's first interactive agent)")
	cmd.Flags().BoolVar(&noInject, "no-inject", false, "upload the file but don't inject its path into the agent's prompt")
	return cmd
}
